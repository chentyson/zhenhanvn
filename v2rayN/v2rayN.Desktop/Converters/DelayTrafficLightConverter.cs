using Avalonia.Data.Converters;

namespace v2rayN.Desktop.Converters;

/// <summary>
/// Colour of the status bar traffic light that sits left of the "current delay" text.
/// Mirrors the WPF <c>DelayTrafficLight</c>: no valid delay (-1) is red, a delay under
/// <see cref="WarningThreshold" /> is green, anything from <see cref="WarningThreshold" /> up is yellow.
/// </summary>
public class DelayTrafficLightConverter : IValueConverter
{
    public const int WarningThreshold = 800;

    private static readonly IBrush Red = new SolidColorBrush(Color.FromRgb(0xE5, 0x39, 0x35));
    private static readonly IBrush Green = new SolidColorBrush(Color.FromRgb(0x43, 0xA0, 0x47));
    private static readonly IBrush Yellow = new SolidColorBrush(Color.FromRgb(0xF9, 0xA8, 0x25));

    public object? Convert(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        var delay = value is int number ? number : value.ToString().ToInt();

        return delay switch
        {
            <= 0 => Red,
            < WarningThreshold => Green,
            _ => Yellow,
        };
    }

    public object? ConvertBack(object? value, Type targetType, object? parameter, CultureInfo culture)
    {
        return null;
    }
}
