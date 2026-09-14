package com.tyrshand.voice

import android.app.role.RoleManager
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

    AsyncFunction("isDefaultAssistant") {
      isDefaultAssistant(requireContext())
    }

    AsyncFunction("openAssistantSettings") {
      openAssistantSettings(requireContext())
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
    intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
    context.startActivity(intent)
  }
}
