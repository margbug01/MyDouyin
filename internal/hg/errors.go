package hg

import (
	"context"
	"errors"
	"net"
	"net/url"

	"github.com/beilunyang/openhongguo/sdk"
)

type ErrorKind int

const (
	ErrKindNone ErrorKind = iota
	ErrKindAnonymousLimited
	ErrKindDeviceRejected
	ErrKindSignerUnavailable
	ErrKindRateLimited
	ErrKindContentUnavailable
	ErrKindNetwork
	ErrKindOther
)

type ErrorInfo struct {
	Kind            ErrorKind
	Message         string
	Retryable       bool
	CanResetDevice  bool
	CanOpenSettings bool
}

func ClassifyError(err error) ErrorInfo {
	if err == nil {
		return ErrorInfo{Kind: ErrKindNone}
	}

	switch {
	case errors.Is(err, sdk.ErrSignerUnavailable):
		return ErrorInfo{
			Kind:            ErrKindSignerUnavailable,
			Message:         "签名服务不可用，请检查红果签名设置或稍后重试",
			Retryable:       true,
			CanOpenSettings: true,
		}
	case errors.Is(err, sdk.ErrSignatureRejected):
		return ErrorInfo{
			Kind:            ErrKindSignerUnavailable,
			Message:         "请求签名被拒绝，请检查签名服务设置",
			Retryable:       false,
			CanOpenSettings: true,
		}
	case errors.Is(err, sdk.ErrDeviceRejected):
		return ErrorInfo{
			Kind:           ErrKindDeviceRejected,
			Message:        "匿名设备已被拒绝，请重置设备后重试",
			Retryable:      true,
			CanResetDevice: true,
		}
	case errors.Is(err, sdk.ErrAnonymousLimited):
		return ErrorInfo{
			Kind:            ErrKindAnonymousLimited,
			Message:         "匿名访问受限，可尝试重置设备或配置签名服务",
			Retryable:       true,
			CanResetDevice:  true,
			CanOpenSettings: true,
		}
	case errors.Is(err, sdk.ErrAuthenticationRequired), errors.Is(err, sdk.ErrChallengeRequired):
		return ErrorInfo{
			Kind:            ErrKindAnonymousLimited,
			Message:         "当前内容需要验证或登录，匿名模式暂不可访问",
			Retryable:       false,
			CanResetDevice:  true,
			CanOpenSettings: true,
		}
	case errors.Is(err, sdk.ErrRateLimited):
		return ErrorInfo{
			Kind:      ErrKindRateLimited,
			Message:   "请求过于频繁，请稍后再试",
			Retryable: true,
		}
	case errors.Is(err, sdk.ErrContentUnavailable), errors.Is(err, sdk.ErrAlbumUnavailable):
		return ErrorInfo{
			Kind:      ErrKindContentUnavailable,
			Message:   "内容不存在、已下架或暂不可观看",
			Retryable: false,
		}
	case errors.Is(err, sdk.ErrDRMProtected):
		return ErrorInfo{
			Kind:      ErrKindContentUnavailable,
			Message:   "该集受加密保护，暂时无法解析播放密钥",
			Retryable: false,
		}
	case isNetworkError(err):
		return ErrorInfo{
			Kind:            ErrKindNetwork,
			Message:         "网络连接失败或超时，请检查网络或直连代理设置",
			Retryable:       true,
			CanOpenSettings: true,
		}
	case errors.Is(err, context.Canceled):
		return ErrorInfo{
			Kind:      ErrKindNetwork,
			Message:   "请求已取消",
			Retryable: true,
		}
	default:
		return ErrorInfo{
			Kind:      ErrKindOther,
			Message:   err.Error(),
			Retryable: sdk.Retryable(err),
		}
	}
}

func isNetworkError(err error) bool {
	if errors.Is(err, sdk.ErrTransport) || errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	return errors.As(err, &urlErr)
}
