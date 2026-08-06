import { Image } from "react-native";

const userImage = Image.resolveAssetSource(require("../../assets/icon.png")).uri;
const agentImage = Image.resolveAssetSource(require("../../assets/adaptive-icon.png")).uri;

const previewImages: Record<string, string> = {
  "60000000-0000-4000-8000-000000000001": userImage,
  "60000000-0000-4000-8000-000000000003": agentImage,
};

export function previewImageURI(attachmentId: string): string | null {
  return previewImages[attachmentId] ?? null;
}

export function previewMarkdownImageURI(): string {
  return agentImage;
}
