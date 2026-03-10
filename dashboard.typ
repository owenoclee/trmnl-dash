// TRMNL ePaper Dashboard
// Display: 10.3" 1872x1404 @ ~227 PPI, 16 shades of gray
// Compile: typst compile --format png --ppi 227 dashboard.typ

#set page(
  width: 8.247in,
  height: 6.185in,
  margin: (x: 0.45in, y: 0.4in),
  fill: white,
)

#set text(
  size: 12pt,
  fill: black,
)

#let today = datetime.today()

// Header

#grid(
  columns: (1fr, auto),
  align: (bottom + left, bottom + right),
  gutter: 0pt,
  [
    #text(size: 42pt, weight: "bold")[
      #today.display("[weekday repr:long]")
    ]
  ],
  [
    #text(size: 20pt, fill: luma(80))[
      #today.display("[month repr:long] [day padding:none], [year]")
    ]
  ],
)

#v(6pt)
#line(length: 100%, stroke: 1pt + black)
#v(14pt)

// Body

#grid(
  columns: (1fr, 0.6fr),
  gutter: 24pt,

  // Left column
  [
    #text(size: 9pt, weight: "bold", fill: luma(100), tracking: 1.5pt)[FOCUS]
    #v(6pt)
    #text(size: 15pt)[
      Build the TRMNL dashboard pipeline:\
      Typst → PNG → `/api/display`
    ]

    #v(20pt)

    #text(size: 9pt, weight: "bold", fill: luma(100), tracking: 1.5pt)[NOTES]
    #v(6pt)
    #for item in (
      "Page dimensions verified at 1872 × 1404 px",
      "16-shade grayscale palette - no color needed",
      "Compiled at 227 PPI matches physical display",
    ) [
      #grid(
        columns: (10pt, 1fr),
        gutter: 4pt,
        [#text(fill: luma(120))[–]], [#text(size: 11pt)[#item]],
      )
      #v(4pt)
    ]
  ],

  // Right column
  [
    #text(size: 9pt, weight: "bold", fill: luma(100), tracking: 1.5pt)[GRAYSCALE PALETTE]
    #v(8pt)
    #stack(
      spacing: 4pt,
      ..range(16).map(i => {
        let g = i * 17  // 0, 17, 34, … 255
        block(
          width: 100%,
          height: 10pt,
          fill: luma(g),
          stroke: if g > 200 { 0.4pt + luma(180) } else { none },
          radius: 1pt,
        )
      })
    )
  ],
)

// Footer

#v(1fr)
#line(length: 100%, stroke: 0.4pt + luma(180))
#v(5pt)
#grid(
  columns: (1fr, auto),
  [
    #text(size: 8pt, fill: luma(140))[
      trmnl · typst renderer · 1872 × 1404 · 227 PPI
    ]
  ],
  [
    #text(size: 8pt, fill: luma(140))[
      #today.display("[year]-[month]-[day]")
    ]
  ],
)
