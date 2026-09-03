# mxs_autologin (Roundcube plugin)

One-click webmail for MX Sentinel SMTP users: the dashboard mints a single-use token, this
plugin redeems it against the API and logs the browser into the matching IMAP mailbox.

Full setup — including the Dovecot IMAP side and the apid settings — is in
[`docs/webmail-autologin.md`](../../../docs/webmail-autologin.md).

## Install

```bash
cp -r deploy/roundcube/mxs_autologin /path/to/roundcube/plugins/
cd /path/to/roundcube/plugins/mxs_autologin
cp config.inc.php.dist config.inc.php   # then fill in mxs_autologin_secret
```

Enable it in the Roundcube config:

```php
$config['plugins'] = ['mxs_autologin'];
```

With the containerised Roundcube in `/opt/dmarcparser/compose.yaml`, mount it instead of
copying:

```yaml
volumes:
  - /opt/mxsentinel/deploy/roundcube/mxs_autologin:/var/www/html/plugins/mxs_autologin:ro
```

## Files

| File | Contents |
|---|---|
| `mxs_autologin.php` | The plugin: `startup` turns a tokened GET into a login action, `authenticate` supplies the redeemed credentials |
| `config.inc.php.dist` | Template config — API base URL, shared secret, timeout |

## Notes

- The plugin logs to Roundcube's `mxs_autologin` log. It never logs the token or password —
  only why a handoff was refused.
- A token that is expired, already used, or malformed silently falls back to the normal
  login form.
