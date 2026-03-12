// TRMNL ePaper Dashboard
// Display: 10.3" 1872x1404 @ ~227 PPI, 16 shades of gray
// Compile: typst compile --format png --ppi 227 --pages 1 dashboard.typ

#let today    = datetime.today()
#let w-loc    = sys.inputs.at("location", default: "")

#set page(
  width: 8.247in,
  height: 6.185in,
  margin: (x: 0.45in, y: 0.4in),
  fill: white,
)

#set text(size: 12pt, fill: black)

// -- helpers --

#let parse-hourly = raw => if raw == "" {
  range(24).map(_ => 0.0)
} else {
  raw.split(",").map(s => float(s))
}

// Scope data to 7am–10pm (indices 7–22, 16 points)
#let slice-day = arr => arr.slice(7, 23)

// -- weather inputs --

#let w-temp          = sys.inputs.at("temp",          default: "--")
#let w-min           = sys.inputs.at("temp-min",       default: "--")
#let w-max           = sys.inputs.at("temp-max",       default: "--")
#let w-wind          = sys.inputs.at("wind",           default: "--")
#let w-condition     = sys.inputs.at("condition",      default: "")
#let w-hourly-temp   = parse-hourly(sys.inputs.at("hourly-temp",   default: ""))
#let w-hourly-wind   = parse-hourly(sys.inputs.at("hourly-wind",   default: ""))
#let w-hourly-precip = parse-hourly(sys.inputs.at("hourly-precip", default: ""))

// -- icons (drawn with Typst primitives) --

// Thermometer: stem + bulb
#let icon-therm = box(width: 10pt, height: 22pt)[
  #place(top + center, dy: 0pt)[
    #line(start: (0pt, 0pt), end: (0pt, 13pt), stroke: 1.5pt + black)
  ]
  #place(bottom + center)[
    #circle(radius: 4pt, fill: black)
  ]
]

// Wind: three horizontal lines of decreasing length
#let icon-wind = stack(
  spacing: 4pt,
  line(length: 14pt, stroke: 1.5pt + black),
  line(length: 10pt, stroke: 1.5pt + black),
  line(length:  6pt, stroke: 1.5pt + black),
)

// Precipitation: downward-pointing filled triangle
#let icon-rain = polygon(
  fill: black,
  stroke: none,
  (0pt, 0pt), (12pt, 0pt), (6pt, 14pt),
)

// -- sparkline chart --

#let icon-col  =  18pt  // width of icon column
#let icon-gap  =   8pt  // gap between icon and labels
#let label-w   =  22pt  // y-axis label column
#let chart-w   = 320pt  // chart width
#let chart-h   =  28pt  // chart height
#let chart-gap =  20pt  // vertical gap between charts
#let n-day     =  16    // 7am–10pm inclusive
#let day-step  = chart-w / (n-day - 1)

// Time marker indices and labels (every 5h: 7, 12, 17, 22)
#let time-idxs  = (0, 5, 10, 15)
#let time-hours = ("7", "12", "17", "22")

#let sparkline(data, icon, y-unit, y-min: auto, y-max: auto) = {
  let n     = data.len()
  let W     = chart-w
  let step  = if n > 1 { W / (n - 1) } else { W }
  let d-min = if y-min == auto { calc.min(..data) } else { float(y-min) }
  let d-max = if y-max == auto { calc.max(..data) } else { float(y-max) }
  let d-rng = if d-max > d-min { d-max - d-min } else { 1.0 }
  let norm  = v => calc.clamp((float(v) - d-min) / d-rng, 0.0, 1.0)

  grid(
    columns: (icon-col, icon-gap, label-w, chart-w),
    align: (center + horizon, auto, left + top, left + top),
    icon,
    [],
    // y-axis labels: vertically centered on their h-line, right-aligned with gap before line
    box(width: label-w, height: chart-h)[
      #place(top + right, dx: -(label-w / 2 + 2pt), dy: -3pt)[
        #text(size: 8pt, fill: black)[#int(calc.round(d-max))#y-unit]
      ]
      #place(bottom + right, dx: -(label-w / 2 + 2pt), dy: 3pt)[
        #text(size: 8pt, fill: black)[#int(calc.round(d-min))#y-unit]
      ]
    ],
    box(width: W, height: chart-h, clip: false)[
      // fill underneath the line (drawn first, behind grid)
      #let pts      = range(n).map(i => (i * step, chart-h * (1 - norm(data.at(i)))))
      #let fill-pts = pts + ((W, chart-h), (0pt, chart-h))
      #place(top + left)[
        #polygon(fill: luma(225), stroke: none, ..fill-pts)
      ]
      // horizontal lines: top, middle, bottom — extend left by half the label column
      #let h-ext = label-w / 2
      #let hline = line(length: W + h-ext, stroke: (paint: luma(180), dash: "dashed", thickness: 0.4pt))
      #place(top + left, dx: -h-ext)[#hline]
      #place(bottom + left, dx: -h-ext)[#hline]
      #place(top + left, dx: -h-ext, dy: chart-h * 0.5)[#hline]
      // connecting lines
      #for i in range(n - 1) {
        let x1 = i * step
        let y1 = chart-h * (1 - norm(data.at(i)))
        let x2 = (i + 1) * step
        let y2 = chart-h * (1 - norm(data.at(i + 1)))
        place(top + left, dx: x1, dy: y1)[
          #line(start: (0pt, 0pt), end: (x2 - x1, y2 - y1), stroke: 1pt + black)
        ]
      }
    ],
  )
}

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

#v(14pt)

// -- charts with shared vertical time lines --

#let charts-h  = 3 * chart-h + 2 * chart-gap  // total stacked height
#let vert-ext  = 14pt                           // line extension below last chart
#let chart-x   = icon-col + icon-gap + label-w  // x offset to chart area

#block(width: 100%, height: charts-h + vert-ext + 10pt)[
  // three sparklines at known vertical offsets
  #place(top + left, dy: 0pt)[
    #sparkline(slice-day(w-hourly-temp),   icon-therm, "°C")
  ]
  #place(top + left, dy: chart-h + chart-gap)[
    #sparkline(slice-day(w-hourly-wind),   icon-wind,  "mph")
  ]
  #place(top + left, dy: 2 * (chart-h + chart-gap))[
    #sparkline(slice-day(w-hourly-precip), icon-rain,  "%", y-min: 0, y-max: 100)
  ]
  // shared vertical time lines spanning all three charts
  #let vline-stroke = (paint: luma(180), dash: "dashed", thickness: 0.4pt)
  #for i in range(time-idxs.len()) {
    let x = chart-x + time-idxs.at(i) * day-step
    // line from top of first chart to vert-ext below last chart
    place(top + left, dx: x)[
      #line(start: (0pt, 0pt), end: (0pt, charts-h + vert-ext), stroke: vline-stroke)
    ]
    // time label to the right of the line, bottom edge flush with line bottom
    place(top + left, dx: x + 2pt, dy: charts-h + vert-ext - 10pt)[
      #text(size: 8pt, fill: black)[#time-hours.at(i)]
    ]
  }
]
