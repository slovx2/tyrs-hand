package appserverhub

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
	"initialize":        methodLocal,
	"runtime/info":      methodForward,
	"thread/turns/list": methodForward,
	"thread/items/list": methodForward,

	// 0.157.1 新增接口。共享 CODEX_HOME 的配置仍遵循单用户原生语义。
	"server/diagnostics":           methodForward,
	"userVerification/status":      methodForward,
	"userVerification/enroll":      methodForward,
	"userVerification/delete":      methodForward,
	"userVerification/verify":      methodForward,
	"userVerification/cancel":      methodForward,
	"thread/queue/add":             methodControlled,
	"thread/queue/list":            methodForward,
	"thread/queue/update":          methodForward,
	"thread/queue/delete":          methodControlled,
	"thread/queue/reorder":         methodForward,
	"thread/queue/start":           methodControlled,
	"thread/attachment/add":        methodForward,
	"thread/attachment/list":       methodForward,
	"thread/attachment/remove":     methodForward,
	"memory/status":                methodForward,
	"rollout/compress":             methodForward,
	"thread/revert":                methodControlled,
	"project/list":                 methodForward,
	"project/read":                 methodForward,
	"project/create":               methodForward,
	"project/import":               methodForward,
	"project/update":               methodForward,
	"project/move":                 methodForward,
	"project/delete":               methodForward,
	"plugin/reconcile":             methodForward,
	"turn/settings/update":         methodForward,
	"thread/timeline/list":         methodForward,
	"account/gatewayOAuth/read":    methodForward,
	"account/gatewayOAuth/login":   methodForward,
	"account/gatewayOAuth/cancel":  methodForward,
	"mcpServer/event/stream/start": methodForward,
	"mcpServer/event/stream/stop":  methodForward,
	"account/bedrock/discover":     methodForward,
	"account/bedrock/setup":        methodForward,

	"account/rateLimits/read":                  methodForward,
	"account/usage/read":                       methodForward,
	"account/workspaceMessages/read":           methodForward,
	"app/installed":                            methodForward,
	"app/list":                                 methodForward,
	"app/read":                                 methodForward,
	"command/exec":                             methodForward,
	"command/exec/resize":                      methodForward,
	"command/exec/terminate":                   methodForward,
	"command/exec/write":                       methodForward,
	"process/spawn":                            methodForward,
	"process/writeStdin":                       methodForward,
	"process/resizePty":                        methodForward,
	"process/kill":                             methodForward,
	"config/read":                              methodForward,
	"configRequirements/read":                  methodForward,
	"experimentalFeature/list":                 methodForward,
	"externalAgentConfig/detect":               methodForward,
	"externalAgentConfig/import/readHistories": methodForward,
	"externalAgentConfig/import/recordHistory": methodForward,
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
	"model/list":                               methodControlled,
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
	"thread/archive":                           methodControlled,
	"thread/compact/start":                     methodForward,
	"thread/delete":                            methodForward,
	"thread/goal/clear":                        methodForward,
	"thread/goal/get":                          methodForward,
	"thread/goal/set":                          methodForward,
	"thread/inject_items":                      methodForward,
	"thread/metadata/update":                   methodForward,
	"thread/name/set":                          methodForward,
	"thread/rollback":                          methodControlled,
	"thread/section/move":                      methodForward,
	"thread/shellCommand":                      methodForward,
	"thread/settings/update":                   methodForward,
	"thread/unarchive":                         methodControlled,
	"windowsSandbox/setupStart":                methodForward,
	"threadSection/create":                     methodForward,
	"threadSection/delete":                     methodForward,
	"threadSection/list":                       methodForward,
	"threadSection/update":                     methodForward,

	"account/read":       methodControlled,
	"thread/fork":        methodControlled,
	"thread/start":       methodControlled,
	"turn/start":         methodControlled,
	"thread/list":        methodControlled,
	"thread/read":        methodForward,
	"thread/resume":      methodControlled,
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
