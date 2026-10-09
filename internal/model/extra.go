package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// extra_json 只允许这四项有限长度的字符串（doc §10.8）。
//
// 这不是“能明文保存的字段”白名单，而是结构化的账号元数据。
// Token / Cookie / 私钥 / 恢复码只允许进入对应加密字段；
// 无法明确分类时由用户修正或跳过，绝不自动塞进 extra_json。
const (
	ExtraRegion       = "region"
	ExtraSubscription = "subscription"
	ExtraExpires      = "expires"
	ExtraSource       = "source"

	// ExtraValueMaxBytes 是单项最大字节数。
	ExtraValueMaxBytes = 128
)

// ExtraAllowedKeys 是 extra_json 允许出现的全部键。
var ExtraAllowedKeys = []string{ExtraRegion, ExtraSubscription, ExtraExpires, ExtraSource}

// Extra 是 extra_json 的结构化表示。
type Extra struct {
	Region       *string `json:"region,omitempty"`
	Subscription *string `json:"subscription,omitempty"`
	Source       *string `json:"source,omitempty"`
	Expires      *string `json:"expires,omitempty"`
}

// ParseExtra 解析数据库中的 extra_json 字符串。
// 空串返回 nil；含未知键或超长值返回错误。
func ParseExtra(raw string) (*Extra, error) {
	if raw == "" {
		return nil, nil
	}
	dec := json.NewDecoder(bytes.NewReader([]byte(raw)))
	dec.DisallowUnknownFields()
	var e Extra
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("extra_json 含不允许的字段：%w", err)
	}
	if err := e.Validate(); err != nil {
		return nil, err
	}
	if e.IsEmpty() {
		return nil, nil
	}
	return &e, nil
}

// Validate 校验白名单、长度与日期格式。
func (e *Extra) Validate() error {
	if e == nil {
		return nil
	}
	for _, f := range []struct {
		name string
		val  *string
	}{
		{ExtraRegion, e.Region},
		{ExtraSubscription, e.Subscription},
		{ExtraSource, e.Source},
		{ExtraExpires, e.Expires},
	} {
		if f.val == nil {
			continue
		}
		if len(*f.val) > ExtraValueMaxBytes {
			return ErrValidation("extra_value_too_long",
				fmt.Sprintf("extra.%s 超过 %d 字节", f.name, ExtraValueMaxBytes))
		}
	}
	if e.Expires != nil && *e.Expires != "" {
		if _, err := time.Parse("2006-01-02", *e.Expires); err != nil {
			return ErrValidation("extra_expires_invalid", "extra.expires 必须是 YYYY-MM-DD")
		}
	}
	return nil
}

// IsEmpty 报告是否所有字段都为空。
func (e *Extra) IsEmpty() bool {
	if e == nil {
		return true
	}
	empty := func(s *string) bool { return s == nil || *s == "" }
	return empty(e.Region) && empty(e.Subscription) && empty(e.Source) && empty(e.Expires)
}

// JSON 序列化；全空时返回空串（写入 NULL 语义）。
func (e *Extra) JSON() (string, error) {
	if e.IsEmpty() {
		return "", nil
	}
	b, err := json.Marshal(e)
	if err != nil {
		return "", fmt.Errorf("序列化 extra_json 失败：%w", err)
	}
	return string(b), nil
}
