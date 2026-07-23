package codexrelay

import "fmt"

type methodClass uint8

const (
	methodUnknown methodClass = iota
	methodLocal
	methodForward
	methodControlled
	methodBlocked
)

var methodClasses = map[string]methodClass{
	"initialize": methodLocal,

	"account/rateLimits/read":                  methodForward,
	"account/read":                             methodForward,
	"account/usage/read":                       methodForward,
	"account/workspaceMessages/read":           methodForward,
	"app/list":                                 methodForward,
	"command/exec":                             methodForward,
	"command/exec/resize":                      methodForward,
	"command/exec/terminate":                   methodForward,
	"command/exec/write":                       methodForward,
	"config/read":                              methodForward,
	"configRequirements/read":                  methodForward,
	"experimentalFeature/list":                 methodForward,
	"externalAgentConfig/detect":               methodForward,
	"externalAgentConfig/import/readHistories": methodForward,
	"fs/copy":                                  methodForward,
	"fs/createDirectory":                       methodForward,
	"fs/getMetadata":                           methodForward,
	"fs/readDirectory":                         methodForward,
	"fs/readFile":                              methodForward,
	"fs/remove":                                methodForward,
	"fs/unwatch":                               methodForward,
	"fs/watch":                                 methodForward,
	"fs/writeFile":                             methodForward,
	"fuzzyFileSearch":                          methodForward,
	"hooks/list":                               methodForward,
	"mcpServer/resource/read":                  methodForward,
	"mcpServer/tool/call":                      methodForward,
	"mcpServerStatus/list":                     methodForward,
	"model/list":                               methodForward,
	"modelProvider/capabilities/read":          methodForward,
	"permissionProfile/list":                   methodForward,
	"plugin/installed":                         methodForward,
	"plugin/list":                              methodForward,
	"plugin/read":                              methodForward,
	"plugin/share/list":                        methodForward,
	"plugin/skill/read":                        methodForward,
	"skills/list":                              methodForward,
	"thread/loaded/list":                       methodForward,
	"windowsSandbox/readiness":                 methodForward,
	"feedback/upload":                          methodForward,
	"marketplace/add":                          methodForward,
	"marketplace/remove":                       methodForward,
	"marketplace/upgrade":                      methodForward,
	"plugin/install":                           methodForward,
	"plugin/share/checkout":                    methodForward,
	"plugin/share/delete":                      methodForward,
	"plugin/share/save":                        methodForward,
	"plugin/share/updateTargets":               methodForward,
	"plugin/uninstall":                         methodForward,
	"review/start":                             methodForward,
	"thread/approveGuardianDeniedAction":       methodForward,
	"thread/archive":                           methodForward,
	"thread/compact/start":                     methodForward,
	"thread/delete":                            methodForward,
	"thread/goal/clear":                        methodForward,
	"thread/goal/get":                          methodForward,
	"thread/goal/set":                          methodForward,
	"thread/inject_items":                      methodForward,
	"thread/metadata/update":                   methodForward,
	"thread/name/set":                          methodForward,
	"thread/rollback":                          methodForward,
	"thread/shellCommand":                      methodForward,
	"thread/unarchive":                         methodForward,
	"windowsSandbox/setupStart":                methodForward,

	"thread/fork":        methodControlled,
	"thread/start":       methodControlled,
	"turn/start":         methodControlled,
	"thread/list":        methodForward,
	"thread/read":        methodForward,
	"thread/resume":      methodForward,
	"thread/unsubscribe": methodForward,
	"turn/interrupt":     methodForward,
	"turn/steer":         methodControlled,

	// 环境由单个用户独占，Desktop 的账号、配置与插件操作优先保持官方行为。
	// 这些写入会影响共享 CODEX_HOME，后续若增加多租户再在 Control 层收紧。
	"account/login/cancel":                 methodForward,
	"account/login/start":                  methodForward,
	"account/logout":                       methodForward,
	"account/rateLimitResetCredit/consume": methodForward,
	"account/sendAddCreditsNudgeEmail":     methodForward,
	"config/batchWrite":                    methodForward,
	"config/mcpServer/reload":              methodForward,
	"config/value/write":                   methodForward,
	"experimentalFeature/enablement/set":   methodForward,
	"externalAgentConfig/import":           methodForward,
	"mcpServer/oauth/login":                methodForward,
	"skills/config/write":                  methodForward,
	"skills/extraRoots/set":                methodForward,
}

func classifyMethod(method string) methodClass {
	if value, ok := methodClasses[method]; ok {
		return value
	}
	return methodUnknown
}

// ClassifiedMethods 返回当前固定 Codex 版本的显式分类，供真实 schema contract test 使用。
func ClassifiedMethods() map[string]string {
	result := make(map[string]string, len(methodClasses))
	for method, class := range methodClasses {
		result[method] = fmt.Sprintf("%d", class)
	}
	return result
}
