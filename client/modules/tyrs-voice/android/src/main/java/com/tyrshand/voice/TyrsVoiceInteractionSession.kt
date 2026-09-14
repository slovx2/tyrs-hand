package com.tyrshand.voice

import android.content.Intent
import android.os.Bundle
import android.service.voice.VoiceInteractionSession

class TyrsVoiceInteractionSession(context: android.content.Context) :
  VoiceInteractionSession(context) {
  override fun onShow(args: Bundle?, showFlags: Int) {
    super.onShow(args, showFlags)
    val intent = LiveIntents.intent(context, true)
    intent.addFlags(
      Intent.FLAG_ACTIVITY_NEW_TASK or
        Intent.FLAG_ACTIVITY_SINGLE_TOP or
        Intent.FLAG_ACTIVITY_CLEAR_TOP,
    )
    LiveWakeBridge.markWake()
    try {
      startVoiceActivity(intent)
    } catch (_: RuntimeException) {
      // Activity 启动失败时仍结束本次系统 Assistant 会话，避免会话悬挂。
    } finally {
      finish()
    }
  }
}
