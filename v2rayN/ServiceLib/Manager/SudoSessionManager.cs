using ServiceLib.ViewModels;

namespace ServiceLib.Manager;

/// <summary>
/// Owns the sudo credential on the non-Windows builds and decides when the user has to be asked
/// for it. The password is kept in memory only (never written to disk).
/// <para>
/// Every privileged step (macOS system proxy, the TUN core and its kill) asks this class first,
/// in this order: passwordless sudo (<c>sudo -n</c>, i.e. a still-valid timestamp or a NOPASSWD
/// entry) -&gt; the password already held for this session -&gt; a single prompt. While a password
/// is held a keep-alive refreshes the sudo timestamp, so the same dialog does not reappear for
/// the next privileged step.
/// </para>
/// </summary>
public static class SudoSessionManager
{
    private const string _tag = "SudoSessionManager";

    // sudo's own timestamp timeout is 5 minutes; refresh well inside that window.
    private const int _keepAliveIntervalSeconds = 50;

    // The prompt is modal and can be left unanswered (or opened in a hidden window); do not let it
    // hold up a reload / shutdown sequence forever.
    private const int _promptTimeoutSeconds = 120;

    private static readonly SemaphoreSlim _refreshGate = new(1, 1);
    private static Timer? _keepAlive;

    public static bool HasCredential => AppManager.Instance.LinuxSudoPwd.IsNotEmpty();

    /// <summary>
    /// True when sudo can run right now without asking anyone: the timestamp is still valid
    /// (possibly because this app refreshed it) or sudoers grants NOPASSWD.
    /// </summary>
    public static async Task<bool> CanRunWithoutPasswordAsync()
    {
        var result = await Utils.GetCliWrapResult(Global.LinuxBash, ["-c", "sudo -n true"]);
        return result.IsSuccess;
    }

    /// <summary>
    /// Validates a password with <c>sudo -S -v</c> (which also refreshes the sudo timestamp), keeps
    /// it for the session and starts the keep-alive.
    /// </summary>
    public static async Task<bool> StorePasswordAsync(string password)
    {
        if (password.IsNullOrEmpty())
        {
            return false;
        }

        var result = await Utils.GetCliWrapResult(Global.LinuxBash, ["-c", "sudo -S -v"], password);
        if (!result.IsSuccess)
        {
            Logging.SaveLog($"{_tag}: sudo password rejected ({result.FailureText()})");
            return false;
        }

        AppManager.Instance.LinuxSudoPwd = password;
        StartKeepAlive();
        return true;
    }

    public static void ForgetCredential()
    {
        AppManager.Instance.LinuxSudoPwd = string.Empty;
        StopKeepAlive();
    }

    /// <summary>
    /// Called once the app has finished its privileged shutdown steps: the sudo timestamp is no
    /// longer refreshed, so a terminal sudo does not silently keep working after v2rayN is gone.
    /// </summary>
    public static void EndSession()
    {
        StopKeepAlive();
    }

    /// <summary>
    /// Makes sure a usable sudo credential exists without bothering the user more than necessary.
    /// </summary>
    /// <param name="allowPrompt">False on shutdown paths, where no modal dialog may be raised.</param>
    /// <returns>True when a privileged command can be run (passwordless or with the stored password).</returns>
    public static async Task<bool> EnsureCredentialAsync(bool allowPrompt)
    {
        if (await CanRunWithoutPasswordAsync())
        {
            return true;
        }

        if (HasCredential)
        {
            return true;
        }

        if (!allowPrompt)
        {
            return false;
        }

        return (await PromptForPasswordAsync()).IsNotEmpty();
    }

    /// <summary>
    /// Whether a password dialog can actually be raised right now: the handler only exists while
    /// StatusBarView is activated, and a dialog opened against a hidden window is invisible (the
    /// await then never returns).
    /// </summary>
    public static async Task<bool> CanShowPromptAsync()
    {
        if (!StatusBarViewModel.Instance.ProxyPromptReady)
        {
            return false;
        }

        if (AppManager.Instance.IsExiting)
        {
            return false;
        }

        if (!AppManager.Instance.ShowInTaskbar)
        {
            // Bring the main window up so the user can actually authorise the change.
            StatusBarViewModel.Instance.ShowHideWindowRequested.Publish(true);
            await Task.Delay(500);
        }

        return true;
    }

    /// <summary>
    /// Raises the administrator password dialog once and stores what the user typed.
    /// Returns null when it cannot be shown, is cancelled, or is left unanswered.
    /// </summary>
    public static async Task<string?> PromptForPasswordAsync()
    {
        if (!await CanShowPromptAsync())
        {
            Logging.SaveLog($"{_tag}: the sudo password prompt cannot be shown right now (UI not ready or the app is closing)");
            return null;
        }

        var prompt = StatusBarViewModel.Instance.PasswordInputInteraction.HandleSafe(RxVoid.Default);
        if (await Task.WhenAny(prompt, Task.Delay(TimeSpan.FromSeconds(_promptTimeoutSeconds))) != prompt)
        {
            Logging.SaveLog($"{_tag}: sudo password prompt timed out after {_promptTimeoutSeconds}s");
            return null;
        }

        var password = await prompt;
        if (password.IsNullOrEmpty())
        {
            return null;
        }

        // The dialog already verified the password; this refreshes the sudo timestamp for the whole
        // session so the next privileged step needs no prompt.
        if (!await StorePasswordAsync(password))
        {
            return null;
        }

        return password;
    }

    /// <summary>
    /// Keeps the sudo timestamp alive while a password is held. Without it every privileged step
    /// (proxy apply, proxy clear on exit, TUN core start/stop) could need a fresh dialog, which is
    /// what made the app look as if it asked for the password "all the time".
    /// </summary>
    private static void StartKeepAlive()
    {
        if (_keepAlive != null)
        {
            return;
        }

        _keepAlive = new Timer(
            _ => _ = RefreshTimestampAsync(),
            null,
            TimeSpan.FromSeconds(_keepAliveIntervalSeconds),
            TimeSpan.FromSeconds(_keepAliveIntervalSeconds));
    }

    private static void StopKeepAlive()
    {
        _keepAlive?.Dispose();
        _keepAlive = null;
    }

    private static async Task RefreshTimestampAsync()
    {
        if (!await _refreshGate.WaitAsync(0))
        {
            return;
        }

        try
        {
            if (!HasCredential)
            {
                StopKeepAlive();
                return;
            }

            var result = await Utils.GetCliWrapResult(Global.LinuxBash, ["-c", "sudo -S -v"], AppManager.Instance.LinuxSudoPwd);
            if (result.IsSuccess)
            {
                return;
            }

            // The password stopped working (changed, or the user revoked the timestamp with
            // "sudo -k"): drop it so the next privileged step asks again instead of failing silently.
            Logging.SaveLog($"{_tag}: sudo timestamp refresh failed, the stored password is discarded ({result.FailureText()})");
            ForgetCredential();
        }
        finally
        {
            _refreshGate.Release();
        }
    }
}
