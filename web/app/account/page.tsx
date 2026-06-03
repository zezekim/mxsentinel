"use client";

import { useEffect, useState } from "react";
import { me, changePassword, getToken, type Me } from "@/lib/api";
import LoadingError from "@/components/LoadingError";

export default function AccountPage() {
  const [user, setUser] = useState<Me | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [saving, setSaving] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);

  useEffect(() => {
    if (!getToken()) {
      window.location.href = "/login";
      return;
    }
    me()
      .then(setUser)
      .catch((e: unknown) => setError(e instanceof Error ? e.message : String(e)))
      .finally(() => setLoading(false));
  }, []);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setFormError(null);
    setSaved(false);
    if (next.length < 8) {
      setFormError("New password must be at least 8 characters.");
      return;
    }
    if (next !== confirm) {
      setFormError("New password and confirmation do not match.");
      return;
    }
    setSaving(true);
    try {
      await changePassword(current, next);
      setSaved(true);
      setCurrent("");
      setNext("");
      setConfirm("");
    } catch (err: unknown) {
      setFormError(err instanceof Error ? err.message : "Could not change password");
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <h1>Account</h1>
      <p className="section-desc">Your sign-in and security settings.</p>

      <LoadingError loading={loading} error={error} />

      {!loading && !error && (
        <>
          {user && (
            <div className="settings-group" style={{ maxWidth: 640 }}>
              <legend style={{ fontWeight: 600, color: "#64748b", fontSize: "0.85rem", textTransform: "uppercase", letterSpacing: "0.03em" }}>
                Profile
              </legend>
              <div className="field">
                <label>Role</label>
                <span>{user.role}</span>
              </div>
              <div className="field">
                <label>User ID</label>
                <span style={{ fontFamily: "monospace", fontSize: "0.85rem" }}>{user.user_id}</span>
              </div>
            </div>
          )}

          <form className="settings-form" onSubmit={handleSubmit}>
            <fieldset className="settings-group">
              <legend>Change password</legend>
              <div className="field">
                <label htmlFor="current">Current password</label>
                <input
                  id="current"
                  type="password"
                  autoComplete="current-password"
                  value={current}
                  onChange={(e) => setCurrent(e.target.value)}
                  required
                />
              </div>
              <div className="field">
                <label htmlFor="next">New password</label>
                <input
                  id="next"
                  type="password"
                  autoComplete="new-password"
                  value={next}
                  onChange={(e) => setNext(e.target.value)}
                  required
                />
                <span className="field-hint">At least 8 characters.</span>
              </div>
              <div className="field">
                <label htmlFor="confirm">Confirm new password</label>
                <input
                  id="confirm"
                  type="password"
                  autoComplete="new-password"
                  value={confirm}
                  onChange={(e) => setConfirm(e.target.value)}
                  required
                />
              </div>
            </fieldset>

            <div className="settings-actions">
              <button type="submit" className="btn btn-primary" disabled={saving}>
                {saving ? "Updating…" : "Update password"}
              </button>
              {saved && <span className="save-ok">Password updated ✓</span>}
              {formError && (
                <span className="state-msg error" style={{ padding: 0 }}>
                  {formError}
                </span>
              )}
            </div>
          </form>
        </>
      )}
    </>
  );
}
