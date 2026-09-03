"use client";

import { useEffect, useRef, useState } from "react";
import { login, getToken } from "@/lib/api";
import { BrandMark } from "@/components/nav-config";

export default function LoginPage() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const errorRef = useRef<HTMLDivElement>(null);

  // If already authenticated, redirect to home
  useEffect(() => {
    if (getToken()) {
      window.location.href = "/";
    }
  }, []);

  // Move focus to the error summary when a sign-in attempt fails, so the
  // reason is announced rather than left for the user to hunt for.
  useEffect(() => {
    if (error) errorRef.current?.focus();
  }, [error]);

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault();
    setError(null);
    setLoading(true);
    try {
      await login(email.trim(), password);
      window.location.href = "/";
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : "Login failed");
    } finally {
      setLoading(false);
    }
  }

  return (
    <div className="login-container">
      <a href="#sign-in" className="skip-link">
        Skip to sign in
      </a>

      <header className="masthead">
        <div className="masthead-inner">
          <span className="login-brand">
            <span className="brand-icon">{BrandMark}</span>
            <span className="masthead-service">
              <span className="brand-name">MX Sentinel</span>
              <span className="brand-desc">
                Email infrastructure observability
              </span>
            </span>
          </span>
        </div>
      </header>

      <main className="login-main" id="sign-in">
        <div className="login-card">
          <h1 className="login-title">Sign in</h1>
          <p className="login-lede">
            Sign in with the account issued to you by the administrator
            responsible for your tenant. Sessions expire; when yours does you
            will be returned to this page.
          </p>

          {error && (
            <div
              className="error-summary"
              role="alert"
              tabIndex={-1}
              ref={errorRef}
            >
              <h2 className="error-summary-title">There is a problem</h2>
              <p className="error-summary-body">{error}</p>
            </div>
          )}

          <form onSubmit={handleSubmit} className="login-form">
            <div className="login-field">
              <label htmlFor="email">Email address</label>
              <span className="login-hint" id="email-hint">
                The address your account was created with.
              </span>
              <input
                id="email"
                type="email"
                autoComplete="email"
                aria-describedby="email-hint"
                required
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                disabled={loading}
              />
            </div>

            <div className="login-field">
              <label htmlFor="password">Password</label>
              <input
                id="password"
                type="password"
                autoComplete="current-password"
                required
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                disabled={loading}
              />
            </div>

            <button type="submit" className="login-btn" disabled={loading}>
              {loading ? "Signing in…" : "Sign in"}
            </button>
          </form>

          <div className="login-notice">
            <strong>Access is restricted to authorized users.</strong> If you do
            not have an account, or you have lost access to one, contact the
            administrator responsible for your tenant. Accounts cannot be
            self-registered.
          </div>
        </div>
      </main>

      <footer className="login-footer">
        <div className="login-footer-inner">
          <span>MX Sentinel</span>
          <span>Email infrastructure observability</span>
        </div>
      </footer>
    </div>
  );
}
