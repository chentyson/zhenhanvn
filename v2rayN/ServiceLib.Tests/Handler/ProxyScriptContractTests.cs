namespace ServiceLib.Tests.Handler;

/// <summary>
/// Guards the rule the sudo prompts are built on: the user is asked for the administrator password
/// only when the system proxy script says a real change needs root and sudo cannot provide it
/// silently. "Nothing to change" and "already failed for another reason" must never raise a dialog.
/// </summary>
public class ProxyScriptContractTests
{
    [Test]
    public async Task NeedsPassword_ExitCodeAndMarker_ShouldAskForThePassword()
    {
        var result = new CliRunResult
        {
            Executed = true,
            IsSuccess = false,
            ExitCode = ProxyScriptContract.NeedsPasswordExitCode,
            StandardOutput = ProxyScriptContract.NeedsPasswordMarker,
        };

        await ProxyScriptContract.ReportsNeedsPassword(result).Should().BeTrue();
    }

    [Test]
    public async Task NeedsPassword_MarkerOnly_ShouldAskForThePassword()
    {
        // A script that reports the marker with its own exit code still gets a password prompt.
        var result = new CliRunResult
        {
            Executed = true,
            IsSuccess = false,
            ExitCode = 1,
            StandardError = $"some sudo noise: {ProxyScriptContract.NeedsPasswordMarker}",
        };

        await ProxyScriptContract.ReportsNeedsPassword(result).Should().BeTrue();
    }

    [Test]
    public async Task Success_NeedsNoCredentialAndNoPrompt()
    {
        // Covers both password-free outcomes: the state already matched, or sudo -n was accepted.
        var applied = new CliRunResult
        {
            Executed = true,
            IsSuccess = true,
            ExitCode = 0,
            StandardOutput = "No change needed: every network service already proxies to 127.0.0.1:10808, no administrator password required.",
        };

        await ProxyScriptContract.ReportsNeedsPassword(applied).Should().BeFalse();
    }

    [Test]
    public async Task RealFailure_ShouldBeReportedInsteadOfAskingForAPassword()
    {
        var failure = new CliRunResult
        {
            Executed = true,
            IsSuccess = false,
            ExitCode = 1,
            StandardError = "NetworkServicesFramework error: Client does not have privileges to maintain Network settings. Error Code: -10015",
        };

        await ProxyScriptContract.ReportsNeedsPassword(failure).Should().BeFalse();
    }

    [Test]
    public async Task ScriptThatCouldNotStart_ShouldNotAskForAPassword()
    {
        var notStarted = new CliRunResult
        {
            Executed = false,
            IsSuccess = false,
            ExitCode = ProxyScriptContract.NeedsPasswordExitCode,
        };

        await ProxyScriptContract.ReportsNeedsPassword(notStarted).Should().BeFalse();
    }
}
