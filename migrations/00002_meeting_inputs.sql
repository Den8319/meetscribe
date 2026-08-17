-- +goose Up
-- +goose StatementBegin
-- Входные данные встречи: текст или аудио, сохранённые при загрузке.
CREATE TABLE meeting_inputs (
    meeting_id UUID PRIMARY KEY REFERENCES meetings(id) ON DELETE CASCADE,
    input_type TEXT NOT NULL,             -- 'audio' | 'text'
    text       TEXT NOT NULL DEFAULT '',  -- содержимое текстовой встречи
    audio_data BYTEA,                     -- байты аудио (voice/audio файлы)
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS meeting_inputs;
-- +goose StatementEnd
