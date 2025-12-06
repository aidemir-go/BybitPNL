package spotpnl

import (
	"bytes"
	"fmt"
	"sort" 
	"github.com/wcharczuk/go-chart/v2"
)


func GeneratePortfolioBarChart(assetValues map[string]float64) ([]byte, error) {
	type assetPair struct {
		Name  string
		Value float64
	}
	var pairs []assetPair
	for name, value := range assetValues {
			pairs = append(pairs, assetPair{name, value})
	}
	sort.Slice(pairs, func(i, j int) bool {
		return pairs[i].Value > pairs[j].Value
	})


barWidth := 60  // Ширина одного столбца
	chartWidth := 200 + (len(pairs) * barWidth) 
if chartWidth > 2048 {
		chartWidth = 2048
	}


var chartValues []chart.Value
	for _, p := range pairs {
		label := fmt.Sprintf("%s\n$%.1f", p.Name, p.Value) // шаг деления 0.1 для значений
		chartValues = append(chartValues, chart.Value{
			Label: label,
			Value: p.Value,
		})
	}
	




	graph := chart.BarChart{
		Title:      "Распределение активов",
		TitleStyle: chart.Style{},
		Background: chart.Style{Padding: chart.Box{Top: 50, Bottom: 30, Left: 20, Right: 20}},
		Width:      chartWidth, 
		Height:     512,
		BarWidth:   barWidth - 10, 
		Bars:       chartValues,
	}

	buffer := bytes.NewBuffer([]byte{})
	err := graph.Render(chart.PNG, buffer)
	if err != nil {
		return nil, err
	}
	
	return buffer.Bytes(), nil
}
