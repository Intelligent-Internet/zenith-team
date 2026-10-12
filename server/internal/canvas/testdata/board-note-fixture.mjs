// Generate the cross-language wire fixture consumed by board_text_test.go.
// This exercises the third-party codec; it is not a frontend interaction suite.
import * as Y from '../../../../frontend/node_modules/yjs/dist/yjs.mjs';
import { writeFileSync } from 'node:fs';
const itemId = '5b78175d-061b-4986-96e3-54a7d640a113';
const base = new Y.Doc();
const paragraph = new Y.XmlElement('paragraph');
paragraph.setAttribute('blockId', itemId);
const text = new Y.XmlText();
paragraph.insert(0, [text]);
base.getXmlFragment('content').insert(0, [paragraph]);
text.insert(0, 'Xin chào 🌱\n');
const a = new Y.Doc(), b = new Y.Doc();
for (const d of [a,b]) Y.applyUpdate(d, Y.encodeStateAsUpdate(base));
const ta = a.getXmlFragment('content').get(0).get(0);
const tb = b.getXmlFragment('content').get(0).get(0);
const undo = new Y.UndoManager(ta, {trackedOrigins: new Set(['local'])});
a.transact(() => ta.insert(0, 'Ý tưởng\nDòng thứ hai\n'), 'local');
b.transact(() => tb.insert(tb.length, '\nQuyết định: cùng nhau\n'));
const updateA = Y.encodeStateAsUpdate(a, Y.encodeStateVector(base));
const updateB = Y.encodeStateAsUpdate(b, Y.encodeStateVector(base));
Y.applyUpdate(a, updateB, 'remote');
const expected = ta.toString();
const vector = Y.encodeStateVector(a);
undo.undo();
const undoA = Y.encodeStateAsUpdate(a, vector);
const encode = (v) => Buffer.from(v).toString('base64');
writeFileSync(new URL('board-note-v1.json', import.meta.url), JSON.stringify({itemId, base:encode(Y.encodeStateAsUpdate(base)), updateA:encode(updateA), updateB:encode(updateB), undoA:encode(undoA), expected, expectedUndo:ta.toString()}, null, 2) + '\n');
