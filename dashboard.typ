// TRMNL ePaper Dashboard
// Display: 10.3" 1872x1404 @ ~227 PPI, 16 shades of gray
// Compile: typst compile --format png --ppi 227 dashboard.typ

#set page(
  width: 8.247in,
  height: 6.185in,
  margin: (x: 0.45in, y: 0.4in),
  fill: white,
)

#set text(size: 12pt, fill: black)

// -- Weather inputs (injected by server via --input) --

#let w-temp      = sys.inputs.at("temp",      default: "--")
#let w-min       = sys.inputs.at("temp-min",  default: "--")
#let w-max       = sys.inputs.at("temp-max",  default: "--")
#let w-wind      = sys.inputs.at("wind",      default: "--")
#let w-condition = sys.inputs.at("condition", default: "")
#let w-hourly-raw = sys.inputs.at("hourly",   default: "")
#let w-location   = sys.inputs.at("location", default: "")

#let w-hourly = if w-hourly-raw != "" {
  w-hourly-raw.split(",").map(s => float(s))
} else {
  // fallback: flat line at 10°C so the chart renders even without data
  range(24).map(_ => 10.0)
}

// -- Header --

#let today = datetime.today()

#grid(
  columns: (1fr, auto),
  align: (bottom + left, bottom + right),
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
#v(20pt)

// -- Current conditions --

#grid(
  columns: (auto, 1fr),
  gutter: 28pt,
  align: (horizon, horizon),
  [
    #text(size: 88pt, weight: "bold", baseline: 0pt)[#w-temp°C]
  ],
  [
    #if w-condition != "" [
      #text(size: 26pt)[#w-condition]
      #v(6pt)
    ]
    #text(size: 16pt, fill: luma(60))[
      High #w-max°  ·  Low #w-min°  ·  Wind #w-wind mph
    ]
  ],
)

#v(24pt)

// -- Hourly temperature bar chart --

#text(size: 9pt, weight: "bold", fill: luma(120), tracking: 1.5pt)[TEMPERATURE TODAY]

#v(10pt)

#let chart-h  = 100pt
#let bar-w    = 18pt
#let bar-gap  = 3pt

#let h-min = calc.min(..w-hourly)
#let h-max = calc.max(..w-hourly)
#let h-range = if h-max > h-min { h-max - h-min } else { 1.0 }

// Bars — nighttime hours (0–5, 22–23) are lighter
#stack(
  dir: ltr,
  spacing: bar-gap,
  ..w-hourly.enumerate().map(((i, t)) => {
    let ratio  = (t - h-min) / h-range
    let bar-h  = calc.max(ratio * chart-h, 3pt)
    let is-day = i >= 6 and i <= 21
    let color  = if is-day { black } else { luma(160) }
    box(width: bar-w, height: chart-h)[
      #align(bottom)[
        #rect(width: 100%, height: bar-h, fill: color, stroke: none, radius: (top-left: 2pt, top-right: 2pt))
      ]
    ]
  })
)

// Hour labels — every 6 hours
#v(4pt)
#stack(
  dir: ltr,
  spacing: bar-gap,
  ..range(24).map(i => {
    box(width: bar-w)[
      #align(center)[
        #if calc.rem(i, 6) == 0 [
          #text(size: 8pt, fill: luma(100))[#str(i)]
        ]
      ]
    ]
  })
)

// -- Footer --

#v(1fr)
#line(length: 100%, stroke: 0.4pt + luma(180))
#v(5pt)
#grid(
  columns: (1fr, auto),
  [
    #text(size: 8pt, fill: luma(140))[
      #if w-location != "" [#w-location  · ]1872 × 1404  ·  227 PPI
    ]
  ],
  [
    #text(size: 8pt, fill: luma(140))[
      #today.display("[year]-[month]-[day]")
    ]
  ],
)
