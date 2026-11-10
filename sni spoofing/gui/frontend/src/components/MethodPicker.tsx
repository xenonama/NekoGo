import { METHODS } from "../types";

// IMPROVE(U2): one-line hover help per bypass method.
const METHOD_HELP: Record<string, string> = {
  auto: "Rank 15 strategies (decoy shape x real-hello splitting) by recent success and latency, and keep the best.",
  wrong_seq: "Send the fake segment with a wrong sequence number.",
  wrong_seq_ttl: "Wrong sequence + reduced TTL so the DPI sees it but the server drops it.",
  split_seq: "Split the fake ClientHello across two TCP segments.",
  fragmented: "Fragment the fake packet into three overlapping pieces.",
  padding: "Pad the fake segment with extra bytes.",
  hello_split: "Cut the client's REAL ClientHello so no segment holds a whole hostname label.",
  hostfakesplit: "Same-length decoy hostname swap, split in two.",
  fakedsplit: "MD5-signed bad-sequence segment + real hello.",
};

export default function MethodPicker({
  value,
  onChange,
}: {
  value: string;
  onChange: (m: string) => void;
}) {
  return (
    <select
      value={value}
      onChange={(e) => onChange(e.target.value)}
      title={METHOD_HELP[value] ?? "DPI bypass method"}
      className="input cursor-pointer hover:border-accent/60 active:border-accent"
    >
      {METHODS.map((m) => (
        <option key={m} value={m} title={METHOD_HELP[m]} className="bg-surface">
          {m}
        </option>
      ))}
    </select>
  );
}
