using System.Reflection;
using ServiceLib.Common;

namespace v2rayN;

/// <summary>
/// Extracts the xray core that is embedded in the single-file exe (when built with
/// EmbedXrayCore=true) into the bin folder so the app is fully self-contained.
/// Files that already exist on disk are left untouched, so a core updated later via
/// the Check Update page is not overwritten.
/// </summary>
public static class EmbeddedCoreExtractor
{
    private const string _tag = "EmbeddedCoreExtractor";

    // (embedded resource name, path relative to StartupPath())
    private static readonly (string Resource, string RelativePath)[] _entries =
    [
        ("core.xray.exe",   Path.Combine("bin", "xray", "xray.exe")),
        ("core.wintun.dll", Path.Combine("bin", "xray", "wintun.dll")),
        ("core.geoip.dat",  Path.Combine("bin", "geoip.dat")),
        ("core.geosite.dat", Path.Combine("bin", "geosite.dat")),
    ];

    public static void ExtractIfMissing()
    {
        try
        {
            var assembly = Assembly.GetEntryAssembly() ?? Assembly.GetExecutingAssembly();
            var baseDir = Utils.StartupPath();

            foreach (var (resource, relativePath) in _entries)
            {
                var target = Path.Combine(baseDir, relativePath);

                // Skip files that are already present and non-empty.
                if (File.Exists(target) && new FileInfo(target).Length > 0)
                {
                    continue;
                }

                using var stream = assembly.GetManifestResourceStream(resource);
                if (stream == null)
                {
                    continue;
                }

                var dir = Path.GetDirectoryName(target);
                if (!string.IsNullOrEmpty(dir))
                {
                    Directory.CreateDirectory(dir);
                }

                using var file = File.Create(target);
                stream.CopyTo(file);
            }
        }
        catch (Exception ex)
        {
            Logging.SaveLog(_tag, ex);
        }
    }
}
