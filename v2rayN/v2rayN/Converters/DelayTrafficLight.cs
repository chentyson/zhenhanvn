using System.Windows.Media;

namespace v2rayN.Converters;

/// <summary>
/// Colour of the status bar traffic light that sits left of the "current delay" text.
/// Same three states in both UIs (see the Avalonia converter of the same name):
/// no valid delay (-1) is red, a delay under <see cref="WarningThreshold" /> is green,
/// anything from <see cref="WarningThreshold" /> up is yellow.
/// </summary>
public static class DelayTrafficLight
{
    public const int WarningThreshold = 800;

    public static readonly Brush Red = Create(0xE5, 0x39, 0x35);
    public static readonly Brush Green = Create(0x43, 0xA0, 0x47);
    public static readonly Brush Yellow = Create(0xF9, 0xA8, 0x25);

    public static Brush For(int delay)
    {
        return delay switch
        {
            <= 0 => Red,
            < WarningThreshold => Green,
            _ => Yellow,
        };
    }

    private static Brush Create(byte r, byte g, byte b)
    {
        var brush = new SolidColorBrush(Color.FromRgb(r, g, b));
        brush.Freeze();
        return brush;
    }
}
