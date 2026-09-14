package com.tyrshand.voice

import android.content.Intent
import android.speech.RecognitionService
import android.speech.SpeechRecognizer

class TyrsRecognitionService : RecognitionService() {
  override fun onStartListening(intent: Intent?, listener: Callback?) {
    listener?.error(SpeechRecognizer.ERROR_CLIENT)
  }

  override fun onStopListening(listener: Callback?) {
    // API 24-30 require a RecognitionService component for Assistant eligibility.
  }

  override fun onCancel(listener: Callback?) {
    // Tyrs Hand does not receive or forward recognition text.
  }
}
