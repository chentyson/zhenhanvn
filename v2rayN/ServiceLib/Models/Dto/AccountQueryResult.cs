namespace ServiceLib.Models.Dto;

/// <summary>
/// s-ui ZY4 account query response (sub/queryService.go, GET /sub/query).
/// </summary>
public class AccountQueryResponse
{
    public int code { get; set; }
    public string? msg { get; set; }
    public AccountQueryData? data { get; set; }
}

public class AccountQueryData
{
    public string? name { get; set; }
    public long expiry { get; set; }
    public string? expiry_str { get; set; }
    public bool unlimited_time { get; set; }
    public long volume { get; set; }
    public string? volume_str { get; set; }
    public bool unlimited_traffic { get; set; }
    public long used { get; set; }
    public string? used_str { get; set; }
    public long up { get; set; }
    public long down { get; set; }
    public int devs { get; set; }
    public string? email { get; set; }
    public bool enable { get; set; }
}
