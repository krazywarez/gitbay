CREATE TABLE repo_topics (
    repo_id  INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    topic    TEXT NOT NULL,
    PRIMARY KEY (repo_id, topic)
);
CREATE INDEX repo_topics_topic ON repo_topics(topic);
