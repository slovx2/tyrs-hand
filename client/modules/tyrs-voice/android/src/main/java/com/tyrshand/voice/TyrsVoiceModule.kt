package com.tyrshand.voice

import android.app.role.RoleManager
import android.app.Activity
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.os.Build
import android.provider.Settings
import expo.modules.kotlin.modules.Module
import expo.modules.kotlin.modules.ModuleDefinition

class TyrsVoiceModule : Module() {
  override fun definition() = ModuleDefinition {
    Name("TyrsVoice")
    Events("onLiveWake")

    OnCreate {
      LiveWakeBridge.onWake = { sendEvent("onLiveWake", mapOf("wake" to true)) }
    }
    OnDestroy {
      LiveWakeBridge.onWake = null
    }

    AsyncFunction("isDefaultAssistant") {
      isDefaultAssistant(requireContext())
    }

    AsyncFunction("openAssistantSettings") {
      openAssistantSettings(requireContext())
    }

    AsyncFunction("openLive") { wake: Boolean ->
      openLive(wake)
    }

    AsyncFunction("finishLive") {
      finishLive()
    }

    AsyncFunction("takeLiveWake") {
      LiveWakeBridge.consumePendingWake()
    }

    OnActivityResult { _, (requestCode, resultCode, _) ->
      if (requestCode != ASSISTANT_SETTINGS_REQUEST_CODE ||
        resultCode == Activity.RESULT_OK) return@OnActivityResult
      // ColorOS can expose ROLE_ASSISTANT but reject it as non-requestable.
      // Its voice input page remains the supported manual fallback.
      appContext.currentActivity?.startActivity(
        Intent(Settings.ACTION_VOICE_INPUT_SETTINGS),
      )
    }
  }

  private fun requireContext(): Context =
    appContext.reactContext ?: throw IllegalStateException("应用上下文不可用")

  private fun isDefaultAssistant(context: Context): Boolean {
    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
      val roleManager = context.getSystemService(RoleManager::class.java)
      return roleManager?.isRoleHeld(RoleManager.ROLE_ASSISTANT) == true
    }
    return android.service.voice.VoiceInteractionService.isActiveService(
      context,
      ComponentName(context, TyrsVoiceInteractionService::class.java),
    )
  }

  private fun openAssistantSettings(context: Context) {
    val intent = if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
      context.getSystemService(RoleManager::class.java)
        ?.takeIf { it.isRoleAvailable(RoleManager.ROLE_ASSISTANT) }
        ?.createRequestRoleIntent(RoleManager.ROLE_ASSISTANT)
        ?: Intent(Settings.ACTION_VOICE_INPUT_SETTINGS)
    } else {
      Intent(Settings.ACTION_VOICE_INPUT_SETTINGS)
    }
    // RoleManager 的授权页从 calling package 识别申请方，必须由前台 Activity
    // 以 startActivityForResult 启动；用 Context.startActivity 会让 ColorOS 看到空包名。
    appContext.throwingActivity.startActivityForResult(intent, ASSISTANT_SETTINGS_REQUEST_CODE)
  }

  private fun openLive(wake: Boolean) {
    val activity = appContext.throwingActivity
    if (wake) LiveWakeBridge.markWake()
    activity.startActivity(LiveIntents.intent(activity, wake))
  }

  private fun finishLive() {
    val activity = appContext.currentActivity ?: return
    if (activity.javaClass.name != LiveIntents.liveActivityClass(activity)) return
    if (activity.isTaskRoot) {
      activity.startActivity(Intent().setClassName(activity.packageName, LiveIntents.mainActivityClass(activity)))
    }
    activity.finish()
  }

  private companion object {
    const val ASSISTANT_SETTINGS_REQUEST_CODE = 4101
  }
}
