import { useEffect, useRef, useState, type ComponentProps } from "react";

import type { LocalAttachment } from "@/app-server/attachments";
import type { TurnPreferences } from "@/app-server/officialClient";
import { clearDraft, loadDraft, saveDraft } from "@/db/drafts";
import { ChatComposer } from "./ChatComposer";

type Props = Omit<ComponentProps<typeof ChatComposer>, "value" | "onChange" | "attachments" |
  "onAttachmentsChange" | "onSend"> & {
  profileId: string;
  sessionId: string;
  preferences: TurnPreferences | null;
  onDraftPreferences: (preferences: TurnPreferences | null) => void;
  onSendMessage: (text: string, attachments: LocalAttachment[]) => Promise<boolean>;
};

/** 输入与草稿写入留在子组件，逐字输入不再重渲染消息列表。 */
export function ConversationComposer({ profileId, sessionId, preferences,
  onDraftPreferences, onSendMessage, ...props }: Props) {
  const [text, setText] = useState("");
  const [attachments, setAttachments] = useState<LocalAttachment[]>([]);
  const [ready, setReady] = useState(false);
  const scope = `thread:${sessionId}`;
  const saveTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const mounted = useRef(true);
  const latestDraft = useRef({ text, attachments, settings: preferences, ready, sending: props.sending });
  latestDraft.current = { text, attachments, settings: preferences, ready, sending: props.sending };
  useEffect(() => () => {
    // 离页时补写防抖窗口内的最后输入，避免快速返回丢失草稿。
    const draft = latestDraft.current;
    if (draft.ready && !draft.sending) {
      void saveDraft(profileId, scope, draft).catch(() => undefined);
    }
  }, [profileId, scope]);
  useEffect(() => {
    mounted.current = true;
    let canceled = false;
    void loadDraft(profileId, scope).then((draft) => {
      if (canceled) return;
      if (draft) {
        setText(draft.text); setAttachments(draft.attachments);
        onDraftPreferences(draft.settings);
      }
    }).catch(() => undefined).finally(() => { if (!canceled) setReady(true); });
    return () => { canceled = true; mounted.current = false; };
  }, [onDraftPreferences, profileId, scope]);
  useEffect(() => {
    if (!ready || props.sending) return;
    saveTimer.current = setTimeout(() => {
      saveTimer.current = null;
      void saveDraft(profileId, scope, { text, attachments, settings: preferences }).catch(() => undefined);
    }, 150);
    return () => {
      if (saveTimer.current) clearTimeout(saveTimer.current);
      saveTimer.current = null;
    };
  }, [attachments, preferences, profileId, props.sending, ready, scope, text]);

  const send = async () => {
    if (!ready) return;
    if (saveTimer.current) clearTimeout(saveTimer.current);
    if (await onSendMessage(text, attachments)) {
      await clearDraft(profileId, scope).catch(() => undefined);
      if (mounted.current) { setText(""); setAttachments([]); }
    }
  };
  return <ChatComposer {...props} disabled={props.disabled || !ready}
    value={text} onChange={setText} attachments={attachments}
    onAttachmentsChange={setAttachments} onSend={() => void send()} />;
}
