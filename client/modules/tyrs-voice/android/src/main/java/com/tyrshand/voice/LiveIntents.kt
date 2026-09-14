package com.tyrshand.voice

// Live 的 ReactActivity 由 app 的 withLiveIntentForward 插件生成。
// 本模块是 Expo library，没有 ReactActivity 编译依赖，不能把 LiveActivity 放在这里。

import android.content.Context
import android.content.Intent

object LiveIntents {
  const val EXTRA_WAKE = "wake"

  fun liveActivityClass(context: Context): String =
    "${context.packageName}.LiveActivity"

  fun mainActivityClass(context: Context): String =
    "${context.packageName}.MainActivity"

  fun intent(context: Context, wake: Boolean): Intent {
    return Intent().setClassName(context.packageName, liveActivityClass(context))
      .putExtra(EXTRA_WAKE, wake)
  }

  fun isLiveUri(intent: Intent?): Boolean {
    val data = intent?.data ?: return false
    if (data.scheme != "tyrshand") return false
    val host = data.host
    val path = data.path?.trim('/')
    return host == "live" || path == "live"
  }

  fun isWake(intent: Intent?): Boolean {
    if (intent?.getBooleanExtra(EXTRA_WAKE, false) == true) return true
    return intent?.data?.getQueryParameter("wake") == "1"
  }
}
