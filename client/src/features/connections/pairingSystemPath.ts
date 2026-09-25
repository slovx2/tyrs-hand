export function redirectPairingSystemPath({ path }: { path: string; initial: boolean }): string {
  const prefix = "tyrshand://device-pair?";
  // Expo Router 6 会先解码自定义 scheme 的 query，再解析为路由参数。
  // 转成应用内相对路径，保留指纹中的 %2B、名称中的 %26 等原始编码。
  return path.startsWith(prefix) ? `/device-pair?${path.slice(prefix.length)}` : path;
}
