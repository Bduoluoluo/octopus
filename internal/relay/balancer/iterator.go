package balancer

import (
	"fmt"
	"strings"
	"time"

	"github.com/xuanli27/octopus/internal/model"
)

// stickySource identifies why a candidate was promoted to the front.
type stickySource string

const (
	stickySourceNone        stickySource = ""
	stickySourcePreferred   stickySource = "preferred"    // HTTP/WS replay or explicit preference
	stickySourceSessionKeep stickySource = "session_keep" // group session stickiness
)

// Iterator 统一的负载均衡迭代器
// 内部编排：策略排序 + 粘性优先 + 决策追踪
type Iterator struct {
	candidates   []model.GroupItem
	index        int
	stickyIdx    int // 粘性通道在 candidates 中的位置，-1 表示无
	stickyKeyID  int
	stickySource stickySource
	mode         model.GroupMode
	modelName    string // 请求模型名（用于熔断检查）

	// 内嵌追踪
	attempts []model.ChannelAttempt
	count    int
}

// NewIterator 创建负载均衡迭代器
// 自动处理：策略排序 + 粘性通道提前
func NewIterator(group model.Group, apiKeyID int, requestModel string) *Iterator {
	return NewIteratorWithPreference(group, apiKeyID, requestModel, nil)
}

// NewIteratorWithPreference 创建带优先通道偏好的负载均衡迭代器。
// preferred 非空时，会优先把指定通道提前到候选列表最前面。
func NewIteratorWithPreference(group model.Group, apiKeyID int, requestModel string, preferred *SessionEntry) *Iterator {
	b := GetBalancer(group.Mode)
	candidates := b.Candidates(group.Items)

	stickyIdx := -1
	stickyKeyID := 0
	source := stickySourceNone
	if preferred != nil && preferred.ChannelID > 0 {
		for i, item := range candidates {
			if item.ChannelID == preferred.ChannelID {
				if i > 0 {
					preferredItem := candidates[i]
					copy(candidates[1:i+1], candidates[0:i])
					candidates[0] = preferredItem
				}
				stickyIdx = 0
				stickyKeyID = preferred.ChannelKeyID
				source = stickySourcePreferred
				break
			}
		}
	}
	if stickyIdx < 0 && group.SessionKeepTime > 0 {
		stickyTTL := time.Duration(group.SessionKeepTime) * time.Second
		if sticky := GetSticky(apiKeyID, requestModel, stickyTTL); sticky != nil {
			for i, item := range candidates {
				if item.ChannelID == sticky.ChannelID {
					if i > 0 {
						// 将粘性通道移到最前面
						stickyItem := candidates[i]
						copy(candidates[1:i+1], candidates[0:i])
						candidates[0] = stickyItem
					}
					stickyIdx = 0
					stickyKeyID = sticky.ChannelKeyID
					source = stickySourceSessionKeep
					break
				}
			}
		}
	}

	return &Iterator{
		candidates:   candidates,
		index:        -1,
		stickyIdx:    stickyIdx,
		stickyKeyID:  stickyKeyID,
		stickySource: source,
		mode:         group.Mode,
		modelName:    requestModel,
	}
}

// Next 移动到下一个候选，返回 false 表示遍历完成
func (it *Iterator) Next() bool {
	it.index++
	return it.index < len(it.candidates)
}

// Reset starts another pass over the same ordered candidate list.
func (it *Iterator) Reset() {
	it.index = -1
}

// Item 返回当前候选的 GroupItem
func (it *Iterator) Item() model.GroupItem {
	return it.candidates[it.index]
}

// IsSticky 当前候选是否为粘性通道
func (it *Iterator) IsSticky() bool {
	return it.stickyIdx >= 0 && it.index == it.stickyIdx
}

func (it *Iterator) StickyKeyID() int {
	if !it.IsSticky() {
		return 0
	}
	return it.stickyKeyID
}

// Len 返回候选列表长度
func (it *Iterator) Len() int {
	return len(it.candidates)
}

// Index 返回当前迭代位置（0-based）
func (it *Iterator) Index() int {
	return it.index
}

// Mode 返回分组负载均衡模式
func (it *Iterator) Mode() model.GroupMode {
	return it.mode
}

// currentReason builds a human-readable selection reason for the current candidate.
func (it *Iterator) currentReason() string {
	if it.index < 0 || it.index >= len(it.candidates) {
		return ""
	}
	item := it.candidates[it.index]
	parts := []string{
		fmt.Sprintf("mode=%s", groupModeLabel(it.mode)),
		fmt.Sprintf("order=%d/%d", it.index+1, len(it.candidates)),
	}

	switch it.mode {
	case model.GroupModeFailover:
		parts = append(parts, fmt.Sprintf("priority=%d", item.Priority))
	case model.GroupModeWeighted:
		weight := item.Weight
		if weight <= 0 {
			weight = 1
		}
		parts = append(parts, fmt.Sprintf("weight=%d", weight))
	}

	if it.IsSticky() {
		switch it.stickySource {
		case stickySourcePreferred:
			parts = append(parts, "sticky=replay_or_preference")
		case stickySourceSessionKeep:
			parts = append(parts, "sticky=session_keep")
		default:
			parts = append(parts, "sticky=true")
		}
		if it.stickyKeyID > 0 {
			parts = append(parts, fmt.Sprintf("sticky_key=%d", it.stickyKeyID))
		}
	}

	return strings.Join(parts, " ")
}

func groupModeLabel(mode model.GroupMode) string {
	switch mode {
	case model.GroupModeRoundRobin:
		return "round_robin"
	case model.GroupModeRandom:
		return "random"
	case model.GroupModeFailover:
		return "failover"
	case model.GroupModeWeighted:
		return "weighted"
	default:
		return fmt.Sprintf("mode_%d", int(mode))
	}
}

func (it *Iterator) baseAttempt(channelID, channelKeyID int, channelName string) model.ChannelAttempt {
	return model.ChannelAttempt{
		ChannelID:    channelID,
		ChannelKeyID: channelKeyID,
		ChannelName:  channelName,
		ModelName:    it.candidates[it.index].ModelName,
		AttemptNum:   it.count,
		Sticky:       it.IsSticky(),
		Reason:       it.currentReason(),
	}
}

// Skip 记录当前通道被跳过（通道禁用、无Key、类型不兼容等）
func (it *Iterator) Skip(channelID, channelKeyID int, channelName, msg string) {
	it.count++
	attempt := it.baseAttempt(channelID, channelKeyID, channelName)
	attempt.Status = model.AttemptSkipped
	attempt.Msg = msg
	it.attempts = append(it.attempts, attempt)
}

// SkipCircuitBreak 检查熔断状态，若已熔断自动记录（含剩余冷却时间）并返回 true
func (it *Iterator) SkipCircuitBreak(channelID, channelKeyID int, channelName string) bool {
	modelName := it.candidates[it.index].ModelName
	tripped, remaining := IsTripped(channelID, channelKeyID, modelName)
	if !tripped {
		return false
	}
	msg := "circuit breaker tripped"
	if remaining > 0 {
		msg = fmt.Sprintf("circuit breaker tripped, remaining cooldown: %ds", int(remaining.Seconds()))
	}
	it.count++
	attempt := it.baseAttempt(channelID, channelKeyID, channelName)
	attempt.Status = model.AttemptCircuitBreak
	attempt.Msg = msg
	it.attempts = append(it.attempts, attempt)
	return true
}

// StartAttempt 开始一次真实转发尝试，返回 Span 用于记录结果
func (it *Iterator) StartAttempt(channelID, channelKeyID int, channelName string) *AttemptSpan {
	it.count++
	return &AttemptSpan{
		attempt:   it.baseAttempt(channelID, channelKeyID, channelName),
		startTime: time.Now(),
		iter:      it,
	}
}

// Attempts 返回所有决策记录（交给日志模块持久化）
func (it *Iterator) Attempts() []model.ChannelAttempt {
	return it.attempts
}

// AttemptSpan 管理单次通道尝试的生命周期（计时、状态、结果）
type AttemptSpan struct {
	attempt   model.ChannelAttempt
	startTime time.Time
	iter      *Iterator
	ended     bool
}

// End 结束尝试：设置状态，自动计算耗时，追加到 Iterator
func (s *AttemptSpan) End(status model.AttemptStatus, statusCode int, msg string) {
	if s.ended {
		return
	}
	s.ended = true
	s.attempt.Status = status
	s.attempt.Duration = int(time.Since(s.startTime).Milliseconds())
	s.attempt.Msg = msg
	s.iter.attempts = append(s.iter.attempts, s.attempt)
}

// Duration 返回从开始到现在的耗时
func (s *AttemptSpan) Duration() time.Duration {
	return time.Since(s.startTime)
}
