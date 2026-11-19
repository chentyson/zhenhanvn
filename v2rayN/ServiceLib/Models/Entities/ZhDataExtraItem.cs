namespace ServiceLib.Models.Entities;

public class ZhDataExtraItem
{
    public List<string> Servers { get; set; } = [];
    public int Conns { get; set; } = 16;
    public int Spread { get; set; } = 8;
    public int DlConns { get; set; } = -1;
    public int Migrate { get; set; } = 1;
    public int SlotBase { get; set; }
    public bool Verbose { get; set; }
}
