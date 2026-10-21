/** A CSV cell value: text is quoted and guarded, numbers and booleans written as is. */
export type CSVValue = string | number | boolean | null | undefined

// Spreadsheets run a cell that starts with one of these as a formula (CSV
// injection); tab and CR are included because some strip them first.
const formulaStart = /^[=+\-@\t\r]/

/**
 * csvCell renders one value per RFC 4180: a field holding a comma, quote or
 * line break is quoted, with quotes doubled. Text that a spreadsheet would
 * treat as a formula is prefixed with a single quote.
 */
export function csvCell(v: CSVValue): string {
  if (v === null || v === undefined) return ''
  let s = String(v)
  if (typeof v === 'string' && formulaStart.test(s)) s = `'${s}`
  return /[",\r\n]/.test(s) ? `"${s.replaceAll('"', '""')}"` : s
}

/** toCSV joins rows (the first is usually the header) with CRLF line endings. */
export function toCSV(rows: CSVValue[][]): string {
  return rows.map((r) => r.map(csvCell).join(',')).join('\r\n') + '\r\n'
}

/** downloadCSV hands content to the browser as a file download named filename. */
export function downloadCSV(filename: string, content: string) {
  const url = URL.createObjectURL(new Blob([content], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = filename
  document.body.append(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}
