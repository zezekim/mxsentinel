--[[
  mxs_trace.lua — MX Sentinel per-message content + spam-verdict exporter for rspamd.

  Runs as an IDEMPOTENT symbol (after classification, cannot change the verdict) and
  fire-and-forgets each message's captured content to MX Sentinel:

      POST {endpoint}/v1/ingest/message-content
      Authorization: Bearer {token}
      { queue_id, message_id, mail_from, subject, raw_headers, spam_score,
        spam_action, is_spam, symbols: [{name, score}, ...] }

  This is what powers the operator per-email drill-down (Spam Tests + Headers tabs).

  SAFETY: the HTTP call is asynchronous with a short timeout and every failure is swallowed —
  mail flow and the spam verdict NEVER depend on MX Sentinel being reachable. If the endpoint
  is down, the message is simply not captured; delivery is unaffected.

  INSTALL (on the relay host):
    1) cp deploy/rspamd/mxs_trace.lua /etc/rspamd/lua/mxs_trace.lua
    2) set MXS_ENDPOINT + MXS_TOKEN below (token = a WRITE-scoped MX Sentinel API token for
       the relay tenant: `mxctl apikey create --tenant <slug> --scopes write --name rspamd-trace`)
    3) echo "dofile('/etc/rspamd/lua/mxs_trace.lua')" >> /etc/rspamd/rspamd.local.lua
    4) systemctl reload rspamd    (check: journalctl -u rspamd | grep mxs_trace)
]]--

local rspamd_http   = require "rspamd_http"
local rspamd_logger = require "rspamd_logger"
local ucl           = require "ucl"

-- ---- configuration -------------------------------------------------------------------------
local MXS_ENDPOINT = "https://sentinel.squidix.net"  -- MX Sentinel API base (no trailing slash)
local MXS_TOKEN    = "REPLACE_WITH_WRITE_TOKEN"       -- WRITE-scoped token for the relay tenant
local MXS_TIMEOUT  = 2.0                              -- seconds; kept short so nothing stalls
-- --------------------------------------------------------------------------------------------

local N = "mxs_trace"

local function export(task)
  local ok, err = pcall(function()
    if MXS_TOKEN == "REPLACE_WITH_WRITE_TOKEN" or MXS_TOKEN == "" then
      return -- not configured yet; stay silent
    end

    local queue_id = task:get_queue_id()
    if not queue_id or queue_id == "" then
      return -- nothing to key on
    end

    -- Envelope MAIL FROM (may be empty for bounces).
    local mail_from = ""
    local from = task:get_from("smtp")
    if from and from[1] and from[1].addr then
      mail_from = from[1].addr
    end

    -- Subject (content) — from the parsed header.
    local subject = task:get_subject() or (task:get_header("Subject")) or ""

    -- Full verbatim header block (content).
    local raw_headers = task:get_raw_headers() or ""

    -- Spam verdict.
    local score, action = 0.0, "no action"
    local msc = task:get_metric_score("default")
    if msc and msc[1] then score = msc[1] end
    local act = task:get_metric_action("default")
    if act then action = act end
    local is_spam = (action == "reject" or action == "add header"
                     or action == "rewrite subject" or action == "soft reject")

    -- Symbols (the "Spam Tests" tiles): name + weight.
    local symbols = {}
    local all = task:get_symbols_all()
    if all then
      for _, sym in ipairs(all) do
        symbols[#symbols + 1] = { name = sym.name, score = sym.score or 0.0 }
      end
    end

    local payload = {
      queue_id    = queue_id,
      message_id  = task:get_message_id() or "",
      mail_from   = mail_from,
      subject     = subject,
      raw_headers = raw_headers,
      spam_score  = score,
      spam_action = action,
      is_spam     = is_spam,
      symbols     = symbols,
    }

    rspamd_http.request({
      task     = task,
      url      = MXS_ENDPOINT .. "/v1/ingest/message-content",
      method   = "POST",
      timeout  = MXS_TIMEOUT,
      headers  = {
        ["Authorization"] = "Bearer " .. MXS_TOKEN,
        ["Content-Type"]  = "application/json",
      },
      body     = ucl.to_format(payload, "json-compact"),
      callback = function(cb_err, code, _, _)
        if cb_err then
          rspamd_logger.debugm(N, task, "mxs export failed: %s", cb_err)
        elseif code and code >= 300 then
          rspamd_logger.debugm(N, task, "mxs export http %s", code)
        end
      end,
    })
  end)
  if not ok then
    rspamd_logger.debugm(N, task, "mxs_trace error (ignored): %s", err)
  end
end

rspamd_config:register_symbol({
  name     = "MXS_TRACE_EXPORT",
  type     = "idempotent",
  callback = export,
  flags    = "empty,explicit_disable,ignore_passthrough",
})

rspamd_logger.infox(rspamd_config, "mxs_trace: registered per-message exporter -> %s", MXS_ENDPOINT)
