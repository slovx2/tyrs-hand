const MAX_DETAIL_CHARACTERS = 4_000;
const MAX_DETAIL_LINES = 12;

export type ActivityDetailPreview = {
  text: string;
  truncated: boolean;
};

export function activityDetailPreview(detail: string): ActivityDetailPreview {
  const characterLimited = detail.slice(0, MAX_DETAIL_CHARACTERS);
  const lines = characterLimited.split("\n");
  const truncated = characterLimited.length < detail.length || lines.length > MAX_DETAIL_LINES;
  const preview = lines.slice(0, MAX_DETAIL_LINES).join("\n");
  return { text: truncated ? `${preview}\n…` : preview, truncated };
}
