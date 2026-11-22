namespace ServiceLib.Tests;

/// <summary>
/// The data store is shared by the tests that build core configs. Creating a table or writing while
/// another test is inside a transaction breaks that transaction's savepoint, so the tables are
/// created once before the session starts and the rows the builders read are seeded here instead of
/// being lazily written from inside a test.
/// </summary>
public static class TestSessionHooks
{
    [Before(TestSession)]
    public static async Task PrepareDataStoreAsync()
    {
        SQLiteHelper.Instance.CreateTable<SubItem>();
        SQLiteHelper.Instance.CreateTable<ProfileItem>();
        SQLiteHelper.Instance.CreateTable<ServerStatItem>();
        SQLiteHelper.Instance.CreateTable<RoutingItem>();
        SQLiteHelper.Instance.CreateTable<ProfileExItem>();
        SQLiteHelper.Instance.CreateTable<DNSItem>();
        SQLiteHelper.Instance.CreateTable<FullConfigTemplateItem>();

        // ConfigHandler.GetDefaultRouting writes when no routing row is flagged active, and it is on
        // the read path of every core config build.
        if (await SQLiteHelper.Instance.TableAsync<RoutingItem>().CountAsync() == 0)
        {
            await SQLiteHelper.Instance.InsertAsync(new RoutingItem
            {
                Id = "routing-test",
                Remarks = "test-routing",
                Url = string.Empty,
                RuleSet = string.Empty,
                CustomIcon = string.Empty,
                CustomRulesetPath4Singbox = string.Empty,
                DomainStrategy = Global.AsIs,
                DomainStrategy4Singbox = string.Empty,
                Sort = 0,
                IsActive = true,
            });
        }
    }
}
