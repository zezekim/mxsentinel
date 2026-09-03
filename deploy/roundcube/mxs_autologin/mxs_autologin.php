<?php

/**
 * mxs_autologin — one-click webmail from the MX Sentinel dashboard.
 *
 * The dashboard mints a single-use token for an SMTP user (POST /v1/smtp-users/{id}/
 * webmail-session, admin scope) and opens
 *
 *     https://<roundcube>/?_mxs_autologin=<token>
 *
 * This plugin redeems that token exactly once against the MX Sentinel API and logs the
 * browser straight into the matching IMAP mailbox. It never sees a password until the
 * redeem call, and the token dies the moment it is used or its few seconds elapse.
 *
 * The redeem call is authenticated with a shared secret (mxs_autologin_secret) that must
 * match apid's MXS_WEBMAIL_PLUGINSECRET, so a token leaked outside the deployment network
 * cannot be exchanged for credentials by anyone else.
 *
 * Install: copy this directory to <roundcube>/plugins/mxs_autologin, copy
 * config.inc.php.dist to config.inc.php and fill it in, then add 'mxs_autologin' to
 * $config['plugins'] in the Roundcube config. See docs/webmail-autologin.md.
 */
class mxs_autologin extends rcube_plugin
{
    /** Only ever needed while logging in. */
    public $task = 'login';

    /** @var array|null credentials resolved from the token, cached for this request */
    private $credential = null;

    public function init()
    {
        $this->load_config();
        $this->add_hook('startup', [$this, 'startup']);
        $this->add_hook('authenticate', [$this, 'authenticate']);
    }

    /**
     * Turn a plain GET carrying a token into a login action, so Roundcube runs its normal
     * login path (and with it the authenticate hook below) instead of rendering the form.
     */
    public function startup($args)
    {
        if (empty($_SESSION['user_id']) && $this->token() !== null) {
            $args['action'] = 'login';
        }

        return $args;
    }

    /**
     * Supply the credentials Roundcube logs in with. Returning valid=false/abort leaves the
     * normal login form in place, which is what a stale or replayed token should produce.
     */
    public function authenticate($args)
    {
        $token = $this->token();
        if ($token === null) {
            return $args;
        }

        $cred = $this->redeem($token);
        if ($cred === null) {
            $args['abort'] = true;
            return $args;
        }

        $args['user'] = $cred['username'];
        $args['pass'] = $cred['password'];
        $args['host'] = $cred['host'];
        // The browser arrives here by redirect from the dashboard, so it has not been
        // through Roundcube's form and carries no login cookie to check yet.
        $args['cookiecheck'] = false;
        $args['valid'] = true;

        return $args;
    }

    /** The autologin token from the query string, or null when absent/malformed. */
    private function token()
    {
        $token = rcube_utils::get_input_value('_mxs_autologin', rcube_utils::INPUT_GET);
        if (!is_string($token) || $token === '') {
            return null;
        }
        // Tokens are "mxw_<8 hex>_<40 hex>"; reject anything else before it reaches the API.
        if (!preg_match('/^mxw_[0-9a-f]{8}_[0-9a-f]{40}$/', $token)) {
            self::log('rejected malformed autologin token');
            return null;
        }

        return $token;
    }

    /**
     * Exchange the token for IMAP credentials. Returns null on any failure — the caller
     * falls back to the ordinary login form rather than surfacing API detail to the browser.
     *
     * @return array|null ['username' => string, 'password' => string, 'host' => string]
     */
    private function redeem($token)
    {
        if ($this->credential !== null) {
            return $this->credential;
        }

        $rcmail = rcmail::get_instance();
        $api    = rtrim((string) $rcmail->config->get('mxs_autologin_api'), '/');
        $secret = (string) $rcmail->config->get('mxs_autologin_secret');
        if ($api === '' || $secret === '') {
            self::log('not configured: set mxs_autologin_api and mxs_autologin_secret');
            return null;
        }

        $timeout = (int) $rcmail->config->get('mxs_autologin_timeout', 5);
        $body    = json_encode(['token' => $token]);
        $headers = [
            'Content-Type: application/json',
            'X-MXS-Webmail-Secret: ' . $secret,
        ];

        $response = $this->post($api . '/v1/webmail/redeem', $body, $headers, $timeout);
        if ($response === null) {
            return null;
        }

        $data = json_decode($response, true);
        if (!is_array($data) || empty($data['username']) || !isset($data['password'])) {
            self::log('redeem response missing credentials');
            return null;
        }

        // apid returns the host already in Roundcube form ("tls://host" / "ssl://host" /
        // bare host); append the port so it does not fall back to Roundcube's default_port.
        $host = (string) $data['imap_host'];
        if ($host === '') {
            $host = (string) $rcmail->config->get('default_host');
        } elseif (!empty($data['imap_port'])) {
            $host .= ':' . (int) $data['imap_port'];
        }

        $this->credential = [
            'username' => (string) $data['username'],
            'password' => (string) $data['password'],
            'host'     => $host,
        ];

        return $this->credential;
    }

    /**
     * POST to the API, preferring curl and falling back to a stream context so the plugin
     * works on a Roundcube image built without ext-curl. Returns the body, or null.
     */
    private function post($url, $body, array $headers, $timeout)
    {
        if (function_exists('curl_init')) {
            $ch = curl_init($url);
            curl_setopt_array($ch, [
                CURLOPT_POST           => true,
                CURLOPT_POSTFIELDS     => $body,
                CURLOPT_HTTPHEADER     => $headers,
                CURLOPT_RETURNTRANSFER => true,
                CURLOPT_TIMEOUT        => $timeout,
                CURLOPT_CONNECTTIMEOUT => $timeout,
            ]);
            $out    = curl_exec($ch);
            $status = curl_getinfo($ch, CURLINFO_RESPONSE_CODE);
            $err    = curl_error($ch);
            curl_close($ch);

            if ($out === false) {
                self::log('redeem request failed: ' . $err);
                return null;
            }
            if ($status !== 200) {
                self::log('redeem rejected with HTTP ' . $status);
                return null;
            }

            return $out;
        }

        $ctx = stream_context_create(['http' => [
            'method'        => 'POST',
            'header'        => implode("\r\n", $headers),
            'content'       => $body,
            'timeout'       => $timeout,
            'ignore_errors' => true,
        ]]);
        $out = @file_get_contents($url, false, $ctx);
        if ($out === false) {
            self::log('redeem request failed (stream transport)');
            return null;
        }
        // $http_response_header is populated by the stream wrapper.
        if (!empty($http_response_header[0]) && strpos($http_response_header[0], ' 200') === false) {
            self::log('redeem rejected: ' . $http_response_header[0]);
            return null;
        }

        return $out;
    }

    /** Never log the token or the password — only why a handoff did not happen. */
    private static function log($message)
    {
        rcube::write_log('mxs_autologin', $message);
    }
}
