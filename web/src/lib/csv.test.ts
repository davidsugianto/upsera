import { describe, expect, it } from 'vitest'
import { csvCell, toCSV } from './csv'

describe('csvCell', () => {
  it('leaves plain values alone', () => {
    expect(csvCell('api')).toBe('api')
    expect(csvCell(42)).toBe('42')
    expect(csvCell(-1.5)).toBe('-1.5')
    expect(csvCell(true)).toBe('true')
    expect(csvCell(null)).toBe('')
    expect(csvCell(undefined)).toBe('')
  })

  it('quotes commas, quotes and newlines (RFC 4180)', () => {
    expect(csvCell('a,b')).toBe('"a,b"')
    expect(csvCell('say "hi"')).toBe('"say ""hi"""')
    expect(csvCell('two\nlines')).toBe('"two\nlines"')
    expect(csvCell('cr\r\nlf')).toBe('"cr\r\nlf"')
  })

  it('guards text that a spreadsheet would run as a formula', () => {
    expect(csvCell('=HYPERLINK("x")')).toBe(`"'=HYPERLINK(""x"")"`)
    expect(csvCell('+1')).toBe("'+1")
    expect(csvCell('-cmd')).toBe("'-cmd")
    expect(csvCell('@SUM(A1)')).toBe("'@SUM(A1)")
    expect(csvCell('\t=1')).toBe("'\t=1")
    // Numbers are data, not text: a negative number stays a number.
    expect(csvCell(-3)).toBe('-3')
  })
})

describe('toCSV', () => {
  it('joins rows with CRLF and ends with one', () => {
    expect(toCSV([['Monitor', 'Checks'], ['a, b', 3], ['=x', null]])).toBe('Monitor,Checks\r\n"a, b",3\r\n\'=x,\r\n')
  })
})
