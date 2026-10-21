package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/Intelligent-Internet/zenith-team/server/internal/authfile"
	"github.com/Intelligent-Internet/zenith-team/server/internal/daemon"
)

// The auth family owns the operator's session: sign in, sign out, and
// explain the session and effective server without printing a secret.
func newAuthCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Sign in to Zenith Team and inspect the session",
	}
	login := &cobra.Command{
		Use:   "login",
		Short: "Authenticate against the Zenith Team server and persist the session",
		Args:  cobra.NoArgs,
		RunE:  runLogin,
	}
	f := login.Flags()
	f.String("server", "", "Zenith Team server URL (default: ZENITH_TEAM_SERVER_URL or the saved config)")
	f.String("email", "", "account email (prompted when omitted)")
	f.String(
		"password",
		"",
		"account password (prompted when omitted; prefer the prompt so the password stays out of shell history)",
	)
	f.Bool("google", false, "sign in with Google in your browser instead of a password")
	f.String("code", "", "redeem a desktop handoff code minted by an already signed-in Zenith Team session")

	logout := &cobra.Command{
		Use:   "logout",
		Short: "End the saved session on the server and forget it locally",
		Args:  cobra.NoArgs,
		RunE:  runLogout,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Show the session state, the effective server and how they were selected",
		Args:  cobra.NoArgs,
		RunE:  runAuthStatus,
	}
	whoami := &cobra.Command{
		Use:   "whoami",
		Short: "Show the signed-in account and its companies",
		Args:  cobra.NoArgs,
		RunE:  runWhoami,
	}
	operatorOnly(login)
	operatorOnly(logout)
	cmd.AddCommand(login, logout, status, whoami)
	return cmd
}

func runLogout(cmd *cobra.Command, _ []string) error {
	path, err := daemon.CredentialPath()
	if err != nil {
		return err
	}
	credential, err := authfile.Load(path)
	if errors.Is(err, authfile.ErrNotFound) {
		if err := forgetDaemonSession(cmd.Context()); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), "not signed in")
		return nil
	}
	if err != nil {
		return fmt.Errorf("read Zenith Team session: %w", err)
	}
	// Best effort on the server: the point of logout is that the refresh
	// token stops working; a server that cannot be reached still loses the
	// local copy, which is what the operator asked for.
	if endpoint, err := authfile.SessionURL(credential.APIBaseURL, "/auth/logout"); err == nil {
		body := mustJSON(map[string]any{"client": "desktop", "refreshToken": credential.RefreshToken})
		request, err := http.NewRequestWithContext(
			cmd.Context(),
			http.MethodPost,
			endpoint,
			strings.NewReader(body),
		)
		if err == nil {
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Authorization", "Bearer "+credential.AccessToken)
			if response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request); err != nil {
				fmt.Fprintf(
					cmd.ErrOrStderr(),
					"warning: the server could not be told about the logout: %v\n",
					err,
				)
			} else {
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
				_ = response.Body.Close()
			}
		}
	}
	if err := authfile.WithLock(cmd.Context(), path, func() error {
		err := os.Remove(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}); err != nil {
		return fmt.Errorf("forget Zenith Team session: %w", err)
	}
	// The daemon's own session is this person's too: signing out
	// ends it, so a running daemon stops acting for them.
	if err := forgetDaemonSession(cmd.Context()); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: %v\n", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "Signed out of %s\n", strings.TrimSuffix(credential.APIBaseURL, "/api/v1"))
	return nil
}

// authStatusReport is the non-secret picture of the session. It is what
// `zen doctor` embeds, so the two never disagree.
type authStatusReport struct {
	Mode           string  `json:"mode"`
	Session        string  `json:"session"`
	Server         string  `json:"server"`
	ServerSource   string  `json:"serverSource"`
	CredentialPath string  `json:"credentialPath"`
	Company        string  `json:"company"`
	CompanySource  string  `json:"companySource"`
	AccessExpires  *string `json:"accessExpiresAt"`
	RefreshExpires *string `json:"refreshExpiresAt"`
	RunID          string  `json:"runId,omitempty"`
}

func buildAuthStatusReport(cmd *cobra.Command) (authStatusReport, error) {
	context := resolveCommandContext()
	report := authStatusReport{Mode: context.mode.String()}
	if context.mode == modeAgent {
		report.Session = "run channel (no credential in this shell)"
		report.Company = context.run.CompanyID
		report.CompanySource = "run.json (Agent mode)"
		report.RunID = context.run.RunID
		report.Server = context.run.Endpoint
		report.ServerSource = "daemon loopback endpoint"
		if context.channelErr != nil {
			report.Session = "run channel unavailable: " + context.channelErr.Error()
		}
		return report, nil
	}
	path, err := daemon.CredentialPath()
	if err != nil {
		return report, err
	}
	report.CredentialPath = path
	server, source, err := resolveServerBaseWithSource("")
	if err != nil {
		return report, err
	}
	report.Server, report.ServerSource = server, source
	if companyID, err := resolveCompanyID(cmd, context); err == nil {
		report.Company = companyID
	} else {
		report.Company = "(not set)"
	}
	report.CompanySource = companySource(cmd, context)
	credential, err := authfile.Load(path)
	switch {
	case errors.Is(err, authfile.ErrNotFound):
		report.Session = "signed out"
	case err != nil:
		report.Session = "unreadable: " + err.Error()
	default:
		switch credential.State(time.Now()) {
		case authfile.StateAuthenticated:
			report.Session = "active"
		case authfile.StateRefreshRequired:
			report.Session = "renewing on next request"
		default:
			report.Session = "expired"
		}
		if credential.AccessExpiresAt != nil {
			value := credential.AccessExpiresAt.UTC().Format(time.RFC3339)
			report.AccessExpires = &value
		}
		if credential.RefreshExpiresAt != nil {
			value := credential.RefreshExpiresAt.UTC().Format(time.RFC3339)
			report.RefreshExpires = &value
		}
		// The session records the server it was issued by; that wins over
		// any later config edit, because it is the server the token works on.
		report.Server = strings.TrimSuffix(credential.APIBaseURL, "/api/v1")
		report.ServerSource = "session (auth.json)"
	}
	return report, nil
}

func runAuthStatus(cmd *cobra.Command, _ []string) error {
	report, err := buildAuthStatusReport(cmd)
	if err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if format == outputJSON {
		return printJSON(out, report)
	}
	rows := [][2]string{
		{"mode", report.Mode},
		{"session", report.Session},
		{"server", report.Server + "  (" + report.ServerSource + ")"},
		{"company", report.Company + "  (" + report.CompanySource + ")"},
	}
	if report.CredentialPath != "" {
		rows = append(rows, [2]string{"credential", report.CredentialPath})
	}
	if report.RunID != "" {
		rows = append(rows, [2]string{"run", report.RunID})
	}
	if report.AccessExpires != nil {
		rows = append(rows, [2]string{"access expires", *report.AccessExpires})
	}
	if report.RefreshExpires != nil {
		rows = append(rows, [2]string{"refresh expires", *report.RefreshExpires})
	}
	printKeyValues(out, rows)
	if report.Session == "signed out" || report.Session == "expired" {
		fmt.Fprintln(cmd.ErrOrStderr(), "run `zen auth login` to sign in")
	}
	return nil
}

func runWhoami(cmd *cobra.Command, _ []string) error {
	context := resolveCommandContext()
	if context.mode == modeAgent {
		return runRunContext(cmd, nil)
	}
	transport, err := newOperatorTransport()
	if err != nil {
		return err
	}
	client := &apiClient{context: context, transport: transport}
	ctx, cancel := apiContext()
	defer cancel()
	var user json.RawMessage
	if err := client.get(ctx, "/me", &user); err != nil {
		return err
	}
	var companies json.RawMessage
	if err := client.get(ctx, "/companies", &companies); err != nil {
		return err
	}
	format, err := resolveOutput(cmd, outputTable)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if format == outputJSON {
		return printJSON(out, map[string]json.RawMessage{"user": user, "companies": companies})
	}
	var account struct {
		ID          string `json:"id"`
		Email       string `json:"email"`
		DisplayName string `json:"displayName"`
	}
	if err := json.Unmarshal(user, &account); err != nil {
		return fmt.Errorf("decode account: %w", err)
	}
	printKeyValues(out, [][2]string{
		{"user", dashIfEmpty(account.DisplayName) + " <" + account.Email + ">"},
		{"id", account.ID},
	})
	fmt.Fprintln(out)
	return printCompanyTable(out, companies)
}

func runLogin(cmd *cobra.Command, _ []string) error {
	serverURL, _ := cmd.Flags().GetString("server")
	base, err := resolveServerBase(serverURL)
	if err != nil {
		return err
	}
	if google, _ := cmd.Flags().GetBool("google"); google {
		return runBrowserLogin(cmd, base)
	}
	if code, _ := cmd.Flags().GetString("code"); code != "" {
		return runCodeLogin(cmd, base, code)
	}
	return runPasswordLogin(cmd, base)
}

func runPasswordLogin(cmd *cobra.Command, base string) error {
	out := cmd.OutOrStdout()
	email, _ := cmd.Flags().GetString("email")
	password, _ := cmd.Flags().GetString("password")

	reader := bufio.NewReader(os.Stdin)
	if email == "" {
		fmt.Fprint(out, "Email: ")
		line, err := reader.ReadString('\n')
		if err != nil {
			return fmt.Errorf("read email: %w", err)
		}
		email = strings.TrimSpace(line)
	}
	if email == "" {
		return errors.New("email is required")
	}
	if password == "" {
		fmt.Fprint(out, "Password: ")
		raw, err := term.ReadPassword(syscall.Stdin)
		fmt.Fprintln(out)
		if err != nil {
			return fmt.Errorf("read password: %w", err)
		}
		password = string(raw)
	}
	if password == "" {
		return errors.New("password is required")
	}

	credential, err := loginRequest(cmd.Context(), base, email, password)
	if err != nil {
		return err
	}
	path, err := persistLogin(cmd.Context(), cmd.ErrOrStderr(), base, credential)
	if err != nil {
		return err
	}
	announceLogin(out, "as "+email, base, path)
	return nil
}

// runBrowserLogin is the Google OAuth path; it shares the credential
// verification and persistence tail with the password login.
func runBrowserLogin(cmd *cobra.Command, base string) error {
	out := cmd.OutOrStdout()
	credential, err := runGoogleLogin(cmd.Context(), base, out)
	if err != nil {
		return err
	}
	path, err := persistLogin(cmd.Context(), cmd.ErrOrStderr(), base, credential)
	if err != nil {
		return err
	}
	announceLogin(out, "via Google", base, path)
	return nil
}

// runCodeLogin redeems a handoff code; same shared tail.
func runCodeLogin(cmd *cobra.Command, base, code string) error {
	credential, err := runHandoffLogin(cmd.Context(), base, code)
	if err != nil {
		return err
	}
	path, err := persistLogin(cmd.Context(), cmd.ErrOrStderr(), base, credential)
	if err != nil {
		return err
	}
	announceLogin(cmd.OutOrStdout(), "via handoff code", base, path)
	return nil
}

// resolveServerBase applies the flag > env > saved-config > default ladder.
func resolveServerBase(serverURL string) (string, error) {
	base, _, err := resolveServerBaseWithSource(serverURL)
	return base, err
}

// resolveServerBaseWithSource is resolveServerBase plus the rung that won,
// so status and doctor can explain the selection.
func resolveServerBaseWithSource(serverURL string) (string, string, error) {
	saved, err := daemon.LoadCLIConfig()
	if err != nil {
		return "", "", err
	}
	source := "--server flag"
	if serverURL == "" {
		serverURL = os.Getenv("ZENITH_TEAM_SERVER_URL")
		source = "ZENITH_TEAM_SERVER_URL"
	}
	if serverURL == "" {
		serverURL = saved.ServerURL
		source = "config.json server_url"
	}
	if serverURL == "" {
		serverURL = daemon.DefaultServerURL
		source = "built-in default"
	}
	base, err := daemon.NormalizeServerBaseURL(serverURL)
	if err != nil {
		return "", "", err
	}
	return base, source, nil
}

// persistLogin verifies the fresh credential against /me, stores it at
// CredentialPath, records the server URL in the settings file, and returns
// the credential path for the success message. A credential the server
// refuses must never reach disk: the daemon would fail mid-run instead of
// at login. A daemon session left by another person is ended, so the next
// `zen daemon start` gives the daemon one for this person.
func persistLogin(
	ctx context.Context,
	warn io.Writer,
	base string,
	credential authfile.Credential,
) (string, error) {
	if err := verifyCredential(ctx, credential); err != nil {
		return "", err
	}
	saved, err := daemon.LoadCLIConfig()
	if err != nil {
		return "", err
	}
	path, err := daemon.CredentialPath()
	if err != nil {
		return "", err
	}
	if err := authfile.Save(path, credential); err != nil {
		return "", err
	}
	saved.ServerURL = base
	if err := daemon.SaveCLIConfig(saved); err != nil {
		return "", err
	}
	forgetDaemonSessionOfAnotherPerson(ctx, warn, credential)
	return path, nil
}

func announceLogin(out io.Writer, how, base, path string) {
	fmt.Fprintf(out, "Signed in %s against %s\n", how, base)
	fmt.Fprintf(out, "Session saved to %s; `zen daemon start` gives the daemon its own session from it.\n", path)
}

// verifyCredential calls GET /me with the freshly issued access token.
func verifyCredential(ctx context.Context, credential authfile.Credential) error {
	endpoint, err := authfile.SessionURL(credential.APIBaseURL, "/me")
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+credential.AccessToken)
	req.Header.Set("Accept", "application/json")
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return fmt.Errorf("verify session: %w", err)
	}
	defer resp.Body.Close()
	// Only the status matters here; the drained bytes are discarded, so a read
	// failure is nothing the caller could act on.
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return fmt.Errorf("the server did not accept the new session (status %d)", resp.StatusCode)
	}
	return nil
}

// loginRequest performs POST /api/v1/auth/login as a desktop (bearer) client
// and returns the durable credential: the short-lived access token plus the
// rotating refresh token that lets the daemon renew without another login.
func loginRequest(ctx context.Context, base, email, password string) (authfile.Credential, error) {
	body, err := json.Marshal(map[string]any{
		"email":    email,
		"password": password,
		"client":   "desktop",
	})
	if err != nil {
		return authfile.Credential{}, err
	}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		base+"/api/v1/auth/login",
		bytes.NewReader(body),
	)
	if err != nil {
		return authfile.Credential{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	httpClient := &http.Client{Timeout: 30 * time.Second}
	resp, err := httpClient.Do(request)
	if err != nil {
		return authfile.Credential{}, fmt.Errorf("login request: %w", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		var apiErr struct {
			Detail string `json:"detail"`
		}
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Detail != "" {
			return authfile.Credential{}, fmt.Errorf("login failed: %s", apiErr.Detail)
		}
		return authfile.Credential{}, fmt.Errorf("login failed: status %d", resp.StatusCode)
	}
	var pending struct {
		Verification json.RawMessage `json:"verification"`
	}
	if json.Unmarshal(raw, &pending) == nil && len(pending.Verification) > 0 {
		return authfile.Credential{}, errors.New("login pending email verification; verify the account first")
	}
	credential, err := authfile.DecodeSession(
		authfile.Credential{Version: authfile.CurrentVersion, APIBaseURL: base + "/api/v1"},
		bytes.NewReader(raw),
	)
	if err != nil {
		return authfile.Credential{}, err
	}
	return credential, nil
}

// runGoogleLogin drives the server's Google OAuth desktop flow: a PKCE
// challenge, the browser for consent, and a loopback listener for the
// redirect. It takes the writer because it must tell the user where to go
// mid-flow, before blocking on the browser callback. The completed flow
// returns the same bearer session as a password login, so everything after
// this call is shared.
func runGoogleLogin(ctx context.Context, base string, out io.Writer) (authfile.Credential, error) {
	verifier, challenge, err := pkcePair()
	if err != nil {
		return authfile.Credential{}, err
	}

	// The server only accepts http://127.0.0.1:<port>/ redirects, so bind
	// first and derive the URI from the assigned port.
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		return authfile.Credential{}, fmt.Errorf("bind loopback callback: %w", err)
	}
	defer listener.Close()
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		return authfile.Credential{}, fmt.Errorf("loopback callback bound to %s, not TCP", listener.Addr())
	}
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d/", address.Port)

	httpClient := &http.Client{Timeout: 30 * time.Second}
	var started struct {
		FlowID           string `json:"flowId"`
		State            string `json:"state"`
		AuthorizationURL string `json:"authorizationUrl"`
	}
	if err := postJSON(ctx, httpClient, base+"/api/v1/auth/google/desktop/start", map[string]any{
		"redirectUri":   redirectURI,
		"codeChallenge": challenge,
	}, &started); err != nil {
		return authfile.Credential{}, fmt.Errorf("start google sign-in: %w", err)
	}

	openBrowser(started.AuthorizationURL)
	fmt.Fprintf(
		out,
		"Complete the sign-in in your browser. If it did not open, visit:\n  %s\n",
		started.AuthorizationURL,
	)

	code, err := waitForOAuthCallback(listener, started.State)
	if err != nil {
		return authfile.Credential{}, err
	}

	completion, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		base+"/api/v1/auth/google/desktop/complete",
		strings.NewReader(mustJSON(map[string]any{
			"flowId":      started.FlowID,
			"redirectUri": redirectURI,
			"state":       started.State,
			"code":        code,
			"verifier":    verifier,
		})),
	)
	if err != nil {
		return authfile.Credential{}, err
	}
	completion.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(completion)
	if err != nil {
		return authfile.Credential{}, fmt.Errorf("complete google sign-in: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return authfile.Credential{}, responseError("google sign-in failed", response)
	}
	return authfile.DecodeSession(
		authfile.Credential{Version: authfile.CurrentVersion, APIBaseURL: base + "/api/v1"},
		response.Body,
	)
}

// runHandoffLogin exchanges a desktop handoff code — minted by an
// already-authenticated Zenith Team session — for a bearer session. It is the
// paste-a-credential path for machines where a browser flow is impractical
// (SSH boxes, containers).
func runHandoffLogin(ctx context.Context, base, code string) (authfile.Credential, error) {
	httpClient := &http.Client{Timeout: 30 * time.Second}
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		base+"/api/v1/auth/desktop/handoff/complete",
		strings.NewReader(mustJSON(map[string]any{"code": strings.TrimSpace(code)})),
	)
	if err != nil {
		return authfile.Credential{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return authfile.Credential{}, fmt.Errorf("redeem handoff code: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return authfile.Credential{}, responseError("handoff code was not accepted", response)
	}
	return authfile.DecodeSession(
		authfile.Credential{Version: authfile.CurrentVersion, APIBaseURL: base + "/api/v1"},
		response.Body,
	)
}

// pkcePair returns an S256 verifier/challenge pair in the exact shape the
// server validates: 43-character base64url strings.
func pkcePair() (verifier, challenge string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate PKCE verifier: %w", err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	digest := sha256.Sum256([]byte(verifier))
	challenge = base64.RawURLEncoding.EncodeToString(digest[:])
	return verifier, challenge, nil
}

// waitForOAuthCallback serves the loopback redirect until Google returns the
// user, checks the state echo, and hands back the authorization code.
func waitForOAuthCallback(listener net.Listener, wantState string) (string, error) {
	type callback struct {
		code string
		err  error
	}
	results := make(chan callback, 1)
	server := &http.Server{
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			query := r.URL.Query()
			outcome := callback{code: query.Get("code")}
			switch {
			case query.Get("state") != wantState:
				outcome.err = errors.New(
					"google sign-in returned a mismatched state; run `zen auth login --google` again",
				)
			case query.Get("error") != "":
				outcome.err = fmt.Errorf("google sign-in was not completed: %s", query.Get("error"))
			case outcome.code == "":
				outcome.err = errors.New("google sign-in returned no authorization code")
			}
			if outcome.err != nil {
				http.Error(w, outcome.err.Error(), http.StatusBadRequest)
			} else {
				w.Header().Set("Content-Type", "text/html; charset=utf-8")
				fmt.Fprint(
					w,
					"<html><body><p>Signed in to Zenith Team. You can close this tab.</p></body></html>",
				)
			}
			select {
			case results <- outcome:
			default:
			}
		}),
	}
	// Serve only ever returns a non-nil error, and the deferred Close below is
	// what ends it; the outcome the caller needs arrives on results instead.
	go func() { _ = server.Serve(listener) }()
	defer server.Close()

	select {
	case outcome := <-results:
		return outcome.code, outcome.err
	case <-time.After(5 * time.Minute):
		return "", errors.New("timed out waiting for the browser sign-in")
	}
}

// openBrowser best-effort opens url in the user's browser; the caller always
// prints the URL as the fallback. A variable so tests can stub the real
// browser away.
var openBrowser = func(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}

func postJSON(ctx context.Context, client *http.Client, url string, body, into any) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(mustJSON(body)))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		return responseError("request failed", response)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(into)
}

func mustJSON(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return string(data)
}

func responseError(context string, response *http.Response) error {
	var payload struct {
		Detail string `json:"detail"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload)
	if strings.TrimSpace(payload.Detail) != "" {
		return fmt.Errorf("%s: %s", context, payload.Detail)
	}
	return fmt.Errorf("%s: status %d", context, response.StatusCode)
}
