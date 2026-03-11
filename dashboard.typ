// TRMNL ePaper Dashboard
// Display: 10.3" 1872x1404 @ ~227 PPI, 16 shades of gray
// Compile: typst compile --format png --ppi 227 dashboard.typ

#set page(
  width: 8.247in,
  height: 6.185in,
  margin: (x: 0.45in, y: 0.4in),
  fill: white,
  footer: [
    #line(length: 100%, stroke: 0.4pt + luma(180))
    #v(3pt)
    #grid(
      columns: (1fr, auto),
      text(size: 8pt, fill: luma(140))[
        #if sys.inputs.at("location", default: "") != "" [#sys.inputs.at("location")  · ]1872 × 1404  ·  227 PPI
      ],
      text(size: 8pt, fill: luma(140))[
        #datetime.today().display("[year]-[month]-[day]")
      ],
    )
  ],
)

#set text(size: 12pt, fill: black)

// -- helpers --

#let parse-hourly(raw) = if raw == "" {
  range(24).map(_ => 0.0)
} else {
  raw.split(",").map(s => float(s))
}

// -- weather inputs --

#let w-temp          = sys.inputs.at("temp",          default: "--")
#let w-min           = sys.inputs.at("temp-min",       default: "--")
#let w-max           = sys.inputs.at("temp-max",       default: "--")
#let w-wind          = sys.inputs.at("wind",           default: "--")
#let w-condition     = sys.inputs.at("condition",      default: "")
#let w-location      = sys.inputs.at("location",       default: "")
#let w-hourly-temp   = parse-hourly(sys.inputs.at("hourly-temp",   default: ""))
#let w-hourly-wind   = parse-hourly(sys.inputs.at("hourly-wind",   default: ""))
#let w-hourly-precip = parse-hourly(sys.inputs.at("hourly-precip", default: ""))

// -- sparkline chart --

#let chart-h = 28pt
#let sq-sz   = 3pt

#let sparkline(data, label, unit, y-min: auto, y-max: auto, show-hours: false) = {
  let n     = data.len()
  let d-min = if y-min == auto { calc.min(..data) } else { float(y-min) }
  let d-max = if y-max == auto { calc.max(..data) } else { float(y-max) }
  let d-rng = if d-max > d-min { d-max - d-min } else { 1.0 }
  let norm  = v => calc.clamp((float(v) - d-min) / d-rng, 0.0, 1.0)

  // header row: label + range
  grid(
    columns: (1fr, auto),
    align: bottom,
    text(size: 8pt, weight: "bold", tracking: 1.5pt)[#upper(label)],
    text(size: 8pt, fill: luma(130))[
      #int(calc.round(d-min))–#int(calc.round(d-max)) #unit
    ],
  )
  v(4pt)

  // chart
  layout(size => {
    let W    = size.width
    let step = if n > 1 { W / (n - 1) } else { W }

    box(width: W, height: chart-h, clip: false)[
      // border
      #place(top + left)[
        #rect(width: W, height: chart-h, stroke: 0.5pt + luma(160), fill: none)
      ]
      // horizontal grid lines at 25 / 50 / 75 %
      #for frac in (0.25, 0.5, 0.75) {
        place(top + left, dy: chart-h * (1 - frac))[
          #line(length: W, stroke: (paint: luma(215), dash: "dotted", thickness: 0.3pt))
        ]
      }
      // line segments
      #for i in range(n - 1) {
        let x1 = i * step
        let y1 = chart-h * (1 - norm(data.at(i)))
        let x2 = (i + 1) * step
        let y2 = chart-h * (1 - norm(data.at(i + 1)))
        place(top + left, dx: x1, dy: y1)[
          #line(start: (0pt, 0pt), end: (x2 - x1, y2 - y1), stroke: 1pt + black)
        ]
      }
      // square markers
      #for i in range(n) {
        let x = i * step
        let y = chart-h * (1 - norm(data.at(i)))
        place(top + left, dx: x - sq-sz / 2, dy: y - sq-sz / 2)[
          #square(size: sq-sz, fill: black, stroke: none)
        ]
      }
    ]
  })

  // optional hour labels (only on the bottom chart)
  if show-hours {
    v(4pt)
    layout(size => {
      let W    = size.width
      let step = if n > 1 { W / (n - 1) } else { W }
      box(width: W, height: 10pt)[
        #for i in range(n) {
          if calc.rem(i, 6) == 0 {
            place(top + left, dx: i * step)[
              #text(size: 7pt, fill: luma(130))[#i]
            ]
          }
        }
      ]
    })
  }
}

// -- header --

#let today = datetime.today()

#grid(
  columns: (1fr, auto),
  align: (bottom + left, bottom + right),
  text(size: 42pt, weight: "bold")[
    #today.display("[weekday repr:long]")
  ],
  text(size: 20pt, fill: luma(80))[
    #today.display("[month repr:long] [day padding:none], [year]")
  ],
)

#v(4pt)
#line(length: 100%, stroke: 1pt + black)
#v(10pt)

// -- current conditions --

#grid(
  columns: (auto, 1fr),
  gutter: 20pt,
  align: (horizon, horizon),
  text(size: 64pt, weight: "bold")[#w-temp°C],
  [
    #if w-condition != "" [
      #text(size: 20pt)[#w-condition]
      #v(4pt)
    ]
    #text(size: 13pt, fill: luma(60))[
      High #w-max°  ·  Low #w-min°  ·  Wind #w-wind mph
    ]
  ],
)

#v(8pt)

// -- charts --

#sparkline(w-hourly-temp,   "temperature", "°C")
#v(5pt)
#sparkline(w-hourly-wind,   "wind",        "mph")
#v(5pt)
#sparkline(w-hourly-precip, "precip",      "%",  y-min: 0, y-max: 100, show-hours: true)

