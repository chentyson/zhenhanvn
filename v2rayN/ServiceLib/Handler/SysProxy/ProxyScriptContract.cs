namespace ServiceLib.Handler.SysProxy;

/// <summary>
/// The contract between the app and the built-in system proxy script (proxy_set_osx_sh).
/// <para>
/// Root is only used where it is really needed: the script compares the current macOS state with
/// what is wanted using read-only commands, so it exits 0 when nothing has to change (a restart of
/// v2rayN with the proxy still set) or when sudo is already authenticated, and only reports
/// <see cref="NeedsPasswordExitCode" /> plus <see cref="NeedsPasswordMarker" /> when a change is
/// needed and sudo would have to ask. That is the single case in which the user is bothered with a
/// password dialog; every other non-zero exit is a real failure and is reported as one.
/// </para>
/// </summary>
public static class ProxyScriptContract
{
    public const int NeedsPasswordExitCode = 3;

    public const string NeedsPasswordMarker = "NEEDS_PASSWORD";

    /// <summary>True when the script says "apply this change, but sudo needs a password to do it".</summary>
    public static bool ReportsNeedsPassword(CliRunResult result)
    {
        return result.Executed
            && (result.ExitCode == NeedsPasswordExitCode
                || result.StandardOutput.Contains(NeedsPasswordMarker)
                || result.StandardError.Contains(NeedsPasswordMarker));
    }
}
