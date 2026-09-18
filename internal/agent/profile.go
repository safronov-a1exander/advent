package agent

import (
	"strings"

	"github.com/safronov-a1exander/advent/internal/profile"
)

// Профиль агента (день 12).
//
// Память одиннадцатого дня агент накапливает сам. Профиль — наоборот:
// его пишет человек, агент его только читает и никогда не меняет.
// Поэтому у профиля нет ни служебных вызовов, ни «обновления после
// реплики» — он просто подмешивается в каждый запрос.
//
// Подмешивается двумя блоками, и это разделение важное:
//
//   - блок профиля — кто собеседник, как с ним говорить, чего нельзя.
//     Один на весь разговор, меняется только правкой файла;
//   - блок дороги — какие стадии положены такому запросу. Меняется
//     от запроса к запросу, потому что дорога выбирается по тексту.
//
// Порядок блоков в системном промпте задан раз и навсегда:
//
//	system → профиль → дорога → память (user, task, chat) → [сводка/факты]
//
// Сначала «кто ты и как отвечаешь», потом «где мы находимся», потом
// «что известно». Порядок фиксированный не ради красоты: он определяет
// префикс, а от префикса зависит кэш. Профиль и дорога меняются реже
// памяти, поэтому стоят раньше неё.

// Profile — профиль агента или nil.
func (a *Agent) Profile() *profile.Profile {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.prof
}

// SetProfile подключает профиль. Зовётся пулом при создании и при смене
// профиля в настройках.
func (a *Agent) SetProfile(p *profile.Profile) {
	a.mu.Lock()
	a.prof = p
	a.mu.Unlock()
}

// Pipeline — дорога, по которой пойдёт этот запрос, и есть ли она вообще.
// Нужна интерфейсу: пользователь должен видеть, куда его запрос свернул,
// до того как получит ответ.
func (a *Agent) Pipeline(query string) (profile.Pipeline, bool) {
	a.mu.Lock()
	p := a.prof
	a.mu.Unlock()
	return p.Pick(query)
}

// profileBlocks — блоки профиля и выбранной дороги для системного промпта.
func profileBlocks(p *profile.Profile, pl profile.Pipeline, picked bool) []string {
	var out []string
	if b := p.Block(); b != "" {
		out = append(out, b)
	}
	if picked {
		if b := pl.StageBlock(); b != "" {
			out = append(out, b)
		}
	}
	return out
}

// applyPipeline накладывает дорогу на конфиг запроса: стратегия рассуждения
// и класс модели.
//
// Накладывается на копию конфига, а не на конфиг агента: дорога зависит от
// текста запроса, и запоминать её в агенте значило бы, что следующий вопрос
// молча унаследует стратегию от предыдущего.
func applyPipeline(cfg Config, pl profile.Pipeline, picked bool, catalog []ModelTier) Config {
	if !picked {
		return cfg
	}
	if s := strings.TrimSpace(pl.Strategy); s != "" {
		cfg.Strategy = s
	}
	if t := strings.TrimSpace(pl.Tier); t != "" {
		if model := modelOfTier(catalog, t); model != "" {
			cfg.Model = model
		}
	}
	return cfg
}

// ModelTier — модель и её класс; пул отдаёт агенту каталог провайдера,
// чтобы дорога могла попросить «слабую» или «сильную», не зная имён.
type ModelTier struct {
	ID   string
	Tier string
}

func modelOfTier(catalog []ModelTier, tier string) string {
	for _, m := range catalog {
		if m.Tier == tier {
			return m.ID
		}
	}
	return ""
}

// SetCatalog запоминает каталог моделей провайдера. Без него дорога с
// `tier` не сможет переключить модель и останется на модели агента —
// молча, но предсказуемо.
func (a *Agent) SetCatalog(c []ModelTier) {
	a.mu.Lock()
	a.catalog = append([]ModelTier(nil), c...)
	a.mu.Unlock()
}
