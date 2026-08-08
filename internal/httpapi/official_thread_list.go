package httpapi

func officialThreadListParams(archived bool, cursor any) map[string]any {
	return map[string]any{
		"archived": archived, "limit": 100, "cursor": cursor,
		"sortKey": "updated_at", "sortDirection": "desc",
		"modelProviders": []string{},
	}
}
