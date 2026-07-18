package gql

import "fmt"

type OperationKey string

const (
	OperationGetStreamInfo        OperationKey = "GetStreamInfo"
	OperationClaimCommunityPoints OperationKey = "ClaimCommunityPoints"
	OperationClaimDrop            OperationKey = "ClaimDrop"
	OperationChannelPointsContext OperationKey = "ChannelPointsContext"
	OperationInventory            OperationKey = "Inventory"
	OperationCurrentDrop          OperationKey = "CurrentDrop"
	OperationCampaigns            OperationKey = "Campaigns"
	OperationRewardCampaigns      OperationKey = "RewardCampaigns"
	OperationCampaignDetails      OperationKey = "CampaignDetails"
	OperationAvailableDrops       OperationKey = "AvailableDrops"
	OperationPlaybackAccessToken  OperationKey = "PlaybackAccessToken"
	OperationGameDirectory        OperationKey = "GameDirectory"
	OperationSlugRedirect         OperationKey = "SlugRedirect"
	OperationNotificationsView    OperationKey = "NotificationsView"
	OperationNotificationsList    OperationKey = "NotificationsList"
	OperationNotificationsDelete  OperationKey = "NotificationsDelete"
)

var registry = map[OperationKey]Operation{
	OperationGetStreamInfo: NewOperation(
		"VideoPlayerStreamInfoOverlayChannel",
		"198492e0857f6aedead9665c81c5a06d67b25b58034649687124083ff288597d",
		map[string]any{
			"channel": Required("channel"),
		},
	),
	OperationClaimCommunityPoints: NewOperation(
		"ClaimCommunityPoints",
		"46aaeebe02c99afdf4fc97c7c0cba964124bf6b0af229395f1f6d1feed05b3d0",
		map[string]any{
			"input": map[string]any{
				"claimID":   Required("claimID"),
				"channelID": Required("channelID"),
			},
		},
	),
	OperationClaimDrop: NewOperation(
		"DropsPage_ClaimDropRewards",
		"a455deea71bdc9015b78eb49f4acfbce8baa7ccbedd28e549bb025bd0f751930",
		map[string]any{
			"input": map[string]any{
				"dropInstanceID": Required("dropInstanceID"),
			},
		},
	),
	OperationChannelPointsContext: NewOperation(
		"ChannelPointsContext",
		"374314de591e69925fce3ddc2bcf085796f56ebb8cad67a0daa3165c03adc345",
		map[string]any{
			"channelLogin": Required("channelLogin"),
		},
	),
	OperationInventory: NewOperation(
		"Inventory",
		"d86775d0ef16a63a33ad52e80eaff963b2d5b72fada7c991504a57496e1d8e4b",
		map[string]any{
			"fetchRewardCampaigns": false,
		},
	),
	OperationCurrentDrop: NewOperation(
		"DropCurrentSessionContext",
		"4d06b702d25d652afb9ef835d2a550031f1cf762b193523a92166f40ea3d142b",
		map[string]any{
			"channelID":    Required("channelID"),
			"channelLogin": "",
		},
	),
	OperationCampaigns: NewOperation(
		"ViewerDropsDashboard",
		"5a4da2ab3d5b47c9f9ce864e727b2cb346af1e3ea8b897fe8f704a97ff017619",
		map[string]any{
			"fetchRewardCampaigns": false,
		},
	),
	OperationRewardCampaigns: NewOperation(
		"ViewerDropsDashboard",
		"d9cae7761dafab85908c85e6683cb4201b449e66ac3bb5e894f15ff12aeafaa7",
		map[string]any{
			"fetchRewardCampaigns": true,
		},
	),
	OperationCampaignDetails: NewOperation(
		"DropCampaignDetails",
		"039277bf98f3130929262cc7c6efd9c141ca3749cb6dca442fc8ead9a53f77c1",
		map[string]any{
			"channelLogin": Required("channelLogin"),
			"dropID":       Required("dropID"),
		},
	),
	OperationAvailableDrops: NewOperation(
		"DropsHighlightService_AvailableDrops",
		"782dad0f032942260171d2d80a654f88bdd0c5a9dddc392e9bc92218a0f42d20",
		map[string]any{
			"channelID": Required("channelID"),
		},
	),
	OperationPlaybackAccessToken: NewOperation(
		"PlaybackAccessToken",
		"ed230aa1e33e07eebb8928504583da78a5173989fadfb1ac94be06a04f3cdbe9",
		map[string]any{
			"isLive":     true,
			"isVod":      false,
			"login":      Required("login"),
			"platform":   "web",
			"playerType": "site",
			"vodID":      "",
		},
	),
	OperationGameDirectory: NewOperation(
		"DirectoryPage_Game",
		"86bcceb4e8b1a51256ff8eed8bd8aae4acacf80d737efe904f84f3aeadf8cafd",
		map[string]any{
			"limit":              30,
			"slug":               Required("slug"),
			"imageWidth":         50,
			"includeCostreaming": false,
			"options": map[string]any{
				"broadcasterLanguages": []any{},
				"freeformTags":         nil,
				"includeRestricted":    []any{"SUB_ONLY_LIVE"},
				"recommendationsContext": map[string]any{
					"platform": "web",
				},
				"sort":          "RELEVANCE",
				"systemFilters": []any{},
				"tags":          []any{},
				"requestID":     "JIRA-VXP-2397",
			},
			"sortTypeIsRecency": false,
		},
	),
	OperationSlugRedirect: NewOperation(
		"DirectoryGameRedirect",
		"1f0300090caceec51f33c5e20647aceff9017f740f223c3c532ba6fa59f6b6cc",
		map[string]any{
			"name": Required("name"),
		},
	),
	OperationNotificationsView: NewOperation(
		"OnsiteNotifications_View",
		"e8e06193f8df73d04a1260df318585d1bd7a7bb447afa058e52095513f2bfa4f",
		map[string]any{
			"input": map[string]any{},
		},
	),
	OperationNotificationsList: NewOperation(
		"OnsiteNotifications_ListNotifications",
		"11cdb54a2706c2c0b2969769907675680f02a6e77d8afe79a749180ad16bfea6",
		map[string]any{
			"cursor":                  "",
			"displayType":             "VIEWER",
			"language":                "en",
			"limit":                   10,
			"shouldLoadLastBroadcast": false,
		},
	),
	OperationNotificationsDelete: NewOperation(
		"OnsiteNotifications_DeleteNotification",
		"13d463c831f28ffe17dccf55b3148ed8b3edbbd0ebadd56352f1ff0160616816",
		map[string]any{
			"input": map[string]any{
				"id": "",
			},
		},
	),
}

func Lookup(key OperationKey) (Operation, bool) {
	operation, ok := registry[key]
	if !ok {
		return Operation{}, false
	}

	return operation.Clone(), true
}

func MustLookup(key OperationKey) Operation {
	operation, ok := Lookup(key)
	if !ok {
		panic(fmt.Sprintf("未知 GQL 操作: %s", key))
	}

	return operation
}

func Registry() map[OperationKey]Operation {
	cloned := make(map[OperationKey]Operation, len(registry))
	for key, operation := range registry {
		cloned[key] = operation.Clone()
	}

	return cloned
}
