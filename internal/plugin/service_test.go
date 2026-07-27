package plugin

import "testing"

func TestMergeVerifiedLeads_RequiresObservedSourceAndDeduplicatesWebsite(t *testing.T) {
	t.Parallel()

	task := &Task{
		Observations: map[string]Observation{
			"page_1": {URL: "https://example.com/about"},
		},
	}
	added := mergeVerifiedLeads(task, []Lead{
		{
			CompanyName: "Example Solar",
			Website:     "https://www.example.com",
			SourceURL:   "https://example.com/about",
			Evidence:    "官网说明提供太阳能组件。",
		},
		{
			CompanyName: "Example Solar Duplicate",
			Website:     "https://example.com/contact",
			SourceURL:   "https://example.com/about",
			Evidence:    "同一企业的另一条记录。",
		},
		{
			CompanyName: "Unobserved Company",
			Website:     "https://unobserved.example",
			SourceURL:   "https://unobserved.example/about",
			Evidence:    "来源页面未被任务观察。",
		},
	})

	if len(added) != 1 || len(task.Leads) != 1 {
		t.Fatalf("added = %#v, stored = %#v", added, task.Leads)
	}
	if task.Leads[0].CompanyName != "Example Solar" {
		t.Fatalf("stored lead = %#v", task.Leads[0])
	}
}
