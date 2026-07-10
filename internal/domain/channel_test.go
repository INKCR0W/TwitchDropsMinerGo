package domain

import "testing"

func TestChannelHelpersReflectStreamState(t *testing.T) {
	t.Parallel()

	channel := &Channel{ID: 1, Login: "streamer", DisplayName: "Streamer"}
	if !channel.Offline() {
		t.Fatal("无流且无 pending 状态时应判定为 offline")
	}
	if channel.PendingOnline() {
		t.Fatal("默认不应为 pending 状态")
	}

	channel.PendingStream = true
	if !channel.PendingOnline() {
		t.Fatal("pending 标记后应判定为 pending online")
	}

	game := &Game{ID: 1, Name: "Game"}
	channel.PendingStream = false
	channel.Stream = &Stream{BroadcastID: 1, Game: game, Viewers: 99, Title: "Live"}
	if !channel.Online() {
		t.Fatal("有流时应判定为 online")
	}
	if got := channel.CurrentGame(); got == nil || got.ID != game.ID {
		t.Fatalf("频道当前游戏不匹配: %#v", got)
	}
	if viewers := channel.ViewerCount(); viewers != 99 {
		t.Fatalf("viewer count 不匹配: %d", viewers)
	}
}
