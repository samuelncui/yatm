export const maxTagLength = 128;
export const maxAddedTags = 16;
export const maxNoteLength = 4096;

export const normalizeTags = (values: string[]) => Array.from(new Set(values.map((value) => value.trim().toLowerCase()).filter(Boolean)));
export const unicodeLength = (value: string) => Array.from(value).length;

export const tagDelta = (baseline: string[], values: string[]) => {
  const before = normalizeTags(baseline);
  const after = normalizeTags(values);
  return { addTags: after.filter((tag) => !before.includes(tag)), removeTags: before.filter((tag) => !after.includes(tag)) };
};

export const validateTags = (tags: string[], addedTags: string[]) => {
  if (tags.some((tag) => unicodeLength(tag) > maxTagLength)) return `Each tag can have at most ${maxTagLength} characters.`;
  if (addedTags.length > maxAddedTags) return `You can add at most ${maxAddedTags} tags at once.`;
  return "";
};

export const validateNote = (note: string) => (unicodeLength(note) > maxNoteLength ? `A note can have at most ${maxNoteLength} characters.` : "");
