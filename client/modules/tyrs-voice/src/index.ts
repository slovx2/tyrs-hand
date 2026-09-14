import { requireNativeModule } from "expo-modules-core";

export type TyrsVoiceModule = {
  isDefaultAssistant(): Promise<boolean>;
  openAssistantSettings(): Promise<void>;
};

export default requireNativeModule<TyrsVoiceModule>("TyrsVoice");
