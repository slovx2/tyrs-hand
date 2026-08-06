import { memo } from "react";
import { StyleSheet, View } from "react-native";

import type { Attachment, Message } from "@/types/protocol";
import { CachedMessageImage } from "./CachedMessageImage";

export const AttachmentImageGallery = memo(function AttachmentImageGallery({ attachments, role }: {
  attachments: Attachment[];
  role: Message["role"];
}) {
  if (attachments.length === 0) return null;
  const thumbnail = attachments.length > 1;
  return <View style={thumbnail ? styles.grid : styles.single}>
    {attachments.map((attachment) => <CachedMessageImage key={attachment.id} attachment={attachment}
      filename={attachment.filename} thumbnail={thumbnail}
      testID={`message:image:${role}:${attachment.id}`} />)}
  </View>;
});

const styles = StyleSheet.create({
  single: { width: "100%" },
  grid: { width: "100%", marginTop: 7, flexDirection: "row", flexWrap: "wrap", gap: "2%" },
});
