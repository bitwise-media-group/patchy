// Type-ahead ranking shared by every filter box. A linear scan over
// pre-lowercased keys — fast enough for tens of thousands of repositories
// per keystroke, with no index to keep in sync.

const BOUNDARY = /[/\-_.\s]/;

// locate finds a needle in a key and ranks the match (lower is better). For
// "owner/name" keys the name is searched first: in a single-organisation
// install every key shares the owner, so an owner match says almost
// nothing. Within the name, exact beats prefix beats word start beats
// mid-word; owner-only matches come last. A needle containing "/" matches
// the whole key. Returns at = -1 when there is no match.
export function locate(key: string, needle: string): [at: number, score: number] {
  const slash = needle.includes("/") ? -1 : key.lastIndexOf("/");
  const name = key.slice(slash + 1);
  const n = name.indexOf(needle);
  if (n >= 0) {
    const at = slash + 1 + n;
    if (n === 0) return [at, needle.length === name.length ? 0 : 1];
    return [at, BOUNDARY.test(name[n - 1]) ? 2 : 3];
  }
  const at = key.indexOf(needle);
  if (at < 0) return [-1, -1];
  return [at, at === 0 ? 4 : 5];
}

export interface Ranked<T> {
  item: T;
  // The matched span's start in the original key, for highlighting; -1 for
  // an empty needle.
  at: number;
}

// rank returns the best `limit` matches and the total match count. With an
// empty needle it returns the heaviest items. keys must be lower-cased and
// parallel to items.
export function rank<T>(
  items: T[],
  keys: string[],
  needle: string,
  limit: number,
  weight: (item: T) => number = () => 0,
): [Ranked<T>[], number] {
  if (needle === "") {
    const top = items
      .map((item) => ({ item, w: weight(item) }))
      .sort((a, b) => b.w - a.w)
      .slice(0, limit);
    return [top.map(({ item }) => ({ item, at: -1 })), items.length];
  }
  const scored: [score: number, weight: number, index: number, at: number][] = [];
  for (let i = 0; i < keys.length; i++) {
    if (!keys[i].includes(needle)) continue;
    const [at, s] = locate(keys[i], needle);
    scored.push([s, -weight(items[i]), i, at]);
  }
  scored.sort((a, b) => a[0] - b[0] || a[1] - b[1]);
  return [scored.slice(0, limit).map(([, , i, at]) => ({ item: items[i], at })), scored.length];
}
