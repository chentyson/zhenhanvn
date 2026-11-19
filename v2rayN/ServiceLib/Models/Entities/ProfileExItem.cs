namespace ServiceLib.Models.Entities;

[Serializable]
public class ProfileExItem
{
    [PrimaryKey]
    public string IndexId { get; set; }

    public int Delay { get; set; }
    public decimal Speed { get; set; }
    public int Sort { get; set; }
    public string? Message { get; set; }
    public string? IpInfo { get; set; }
    public long ExpireTime { get; set; }
    public string? ExpireDate { get; set; }
    public string? TrafficInfo { get; set; }
    public long TrafficVolume { get; set; }
    public long TrafficUsed { get; set; }
}
