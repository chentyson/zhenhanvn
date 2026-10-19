using v2rayN.Desktop.Common;
using v2rayN.Desktop.Manager;

namespace v2rayN.Desktop;

internal class Program
{
    public static EventWaitHandle ProgramStarted;

    private static Mutex? _unixSingleInstanceMutex;

    // Initialization code. Don't use any Avalonia, third-party APIs or any
    // SynchronizationContext-reliant code before AppMain is called: things aren't initialized
    // yet and stuff might break.
    [STAThread]
    public static void Main(string[] args)
    {
        if (OnStartup(args) == false)
        {
            Environment.Exit(0);
            return;
        }

        BuildAvaloniaApp()
            .StartWithClassicDesktopLifetime(args);
    }

    private static bool OnStartup(string[]? Args)
    {
        var args = Args ?? [];
        var rebootas = args.Any(t => t == Global.RebootAs);
        if (Utils.IsWindows())
        {
            var exePathKey = Utils.GetMd5(Utils.GetExePath());
            ProgramStarted = new EventWaitHandle(false, EventResetMode.AutoReset, exePathKey, out var bCreatedNew);
            if (!rebootas && !bCreatedNew)
            {
                ProgramStarted.Set();
                return false;
            }
        }
        else
        {
            _unixSingleInstanceMutex = new Mutex(true, "v2rayN", out var bOnlyOneInstance);
            if (!bOnlyOneInstance && rebootas)
            {
                // A restart was requested: the old instance is still exiting. Wait for it
                // to release the single-instance mutex instead of silently dying.
                var deadline = DateTime.UtcNow.AddSeconds(20);
                while (!bOnlyOneInstance && DateTime.UtcNow < deadline)
                {
                    _unixSingleInstanceMutex.Dispose();
                    Thread.Sleep(250);
                    _unixSingleInstanceMutex = new Mutex(true, "v2rayN", out bOnlyOneInstance);
                }
            }
            if (!bOnlyOneInstance)
            {
                _unixSingleInstanceMutex.Dispose();
                _unixSingleInstanceMutex = null;
                return false;
            }
        }

        if (!AppManager.Instance.InitApp())
        {
            return false;
        }

        AppManager.Instance.WindowDialog = new WindowDialog();
        return true;
    }

    // Avalonia configuration, don't remove; also used by visual designer.
    public static AppBuilder BuildAvaloniaApp()
    {
        var builder = AppBuilder.Configure<App>()
           .UsePlatformDetect()
           //.WithInterFont()
           .WithFontByDefault()
#if DEBUG
           .WithDeveloperTools()
#endif
           .LogToTrace()
           .UseReactiveUI(_ => { });

        if (OperatingSystem.IsMacOS())
        {
            var showInDock = Design.IsDesignMode || AppManager.Instance.Config.UiItem.MacOSShowInDock;
            builder = builder.With(new MacOSPlatformOptions { ShowInDock = showInDock });
        }

        return builder;
    }
}
