import ExcelJS from "exceljs";

export type SpreadsheetSheet = {
  name: string;
  rows: string[][];
  rowCount: number;
  columnCount: number;
};

self.onmessage = async (
  event: MessageEvent<{ data: ArrayBuffer; format: "xlsx" | "csv" | "tsv" }>,
) => {
  try {
    if (event.data.format !== "xlsx") {
      const content = new TextDecoder().decode(event.data.data);
      self.postMessage({
        sheets: [
          delimitedSheet(
            content,
            event.data.format === "tsv" ? "\t" : detectCsvSeparator(content),
          ),
        ],
      });
      return;
    }
    const workbook = new ExcelJS.Workbook();
    await workbook.xlsx.load(event.data.data);
    const sheets: SpreadsheetSheet[] = workbook.worksheets
      .filter((sheet) => sheet.state === "visible")
      .map((sheet) => {
        const rowCount = sheet.rowCount;
        const columnCount = sheet.columnCount;
        const rows = Array.from({ length: Math.min(rowCount, 500) }, (_, row) =>
          Array.from({ length: Math.min(columnCount, 50) }, (_, column) => {
            const cell = sheet.getCell(row + 1, column + 1);
            const value = cell.value;
            if (value instanceof Date)
              return value.toISOString().replace(/T00:00:00\.000Z$/, "");
            // ExcelJS reads cached formula results; formulas and links are never executed.
            return cell.text.slice(0, 2000);
          }),
        );
        return { name: sheet.name, rows, rowCount, columnCount };
      });
    self.postMessage({ sheets });
  } catch {
    self.postMessage({
      error:
        "This spreadsheet could not be opened. It may be password-protected or damaged. You can download it to open in Excel.",
    });
  }
};

function detectCsvSeparator(content: string): string {
  let separator = ",";
  let bestConsistency = 0;
  let bestColumnCount = 0;
  for (const candidate of [",", ";", "\t"]) {
    // Sample complete records so quoted delimiters and multiline cells stay intact.
    const rows = delimitedSheet(content, candidate, 20).rows.filter((row) =>
      row.some((cell) => cell.trim()),
    );
    const widths = new Map<number, number>();
    for (const row of rows) {
      if (row.length > 1)
        widths.set(row.length, (widths.get(row.length) ?? 0) + 1);
    }
    for (const [columnCount, count] of widths) {
      const consistency = count / rows.length;
      if (
        consistency > bestConsistency ||
        (consistency === bestConsistency && columnCount > bestColumnCount)
      ) {
        separator = candidate;
        bestConsistency = consistency;
        bestColumnCount = columnCount;
      }
    }
  }
  return separator;
}

function delimitedSheet(
  content: string,
  separator: string,
  rowLimit = Infinity,
): SpreadsheetSheet {
  const rows: string[][] = [];
  let row: string[] = [];
  let cell = "";
  let quoted = false;
  let rowCount = 0;
  let columnCount = 0;
  let columns = 0;
  const finishCell = () => {
    if (columns < 50) row.push(cell);
    columns++;
    cell = "";
  };
  const finishRow = () => {
    finishCell();
    columnCount = Math.max(columnCount, columns);
    if (rowCount < 500) rows.push(row);
    rowCount++;
    row = [];
    columns = 0;
  };
  for (let index = 0; index < content.length && rowCount < rowLimit; index++) {
    const character = content[index];
    if (character === '"') {
      if (quoted && content[index + 1] === '"') {
        if (cell.length < 2000) cell += '"';
        index++;
      } else quoted = !quoted;
    } else if (!quoted && character === separator) finishCell();
    else if (!quoted && (character === "\n" || character === "\r")) {
      if (character === "\r" && content[index + 1] === "\n") index++;
      finishRow();
    } else if (cell.length < 2000) cell += character;
  }
  if (
    rowCount < rowLimit &&
    (cell || columns || (content.length > 0 && !/[\r\n]$/.test(content)))
  )
    finishRow();
  return { name: "Data", rows, rowCount, columnCount };
}
