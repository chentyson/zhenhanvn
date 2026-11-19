namespace ServiceLib.Models.Entities;

public class ZhDataExtraItem
{
    public const int DefaultConns = 32;
    public const int DefaultSpread = 8;
    public const int DefaultDlConns = -1;
    public const int DefaultMigrate = 1;
    public const int DefaultPortSpan = 32;

    public List<string> Servers { get; set; } = [];
    public int Conns { get; set; } = DefaultConns;
    public int Spread { get; set; } = DefaultSpread;
    public int DlConns { get; set; } = DefaultDlConns;
    public int Migrate { get; set; } = DefaultMigrate;
    public int SlotBase { get; set; }
    public bool Verbose { get; set; }
    public bool AutoPorts { get; set; } = true;
    public int PortSpan { get; set; } = DefaultPortSpan;
}
